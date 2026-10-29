package updaters

import (
	"encoding/json"
	"errors"
	"fmt"
)

var errNotFound = errors.New("path not found")

// JSONField sets the string at path (e.g. "$.version") to value.
func JSONField(path, value string) Updater {
	return Func(func(c string, _ bool) (string, error) {
		segs, err := ParsePath(path)
		if err != nil {
			return c, err
		}
		return SetJSON(c, segs, value)
	})
}

// SetJSON replaces the value at segs with a JSON string, keeping all other bytes.
func SetJSON(c string, segs []Segment, value string) (string, error) {
	start, end, err := FindJSON(c, segs)
	if err != nil {
		return c, fmt.Errorf("json %s: %w", pathString(segs), err)
	}
	q, _ := json.Marshal(value)
	return splice(c, start, end, string(q)), nil
}

// GetJSONString returns the string at segs, if present.
func GetJSONString(c string, segs []Segment) (string, bool) {
	start, end, err := FindJSON(c, segs)
	if err != nil {
		return "", false
	}
	var s string
	if json.Unmarshal([]byte(c[start:end]), &s) != nil {
		return "", false
	}
	return s, true
}

// FindJSON returns the byte span of the value at segs.
func FindJSON(c string, segs []Segment) (int, int, error) {
	s := &jscan{b: c}
	s.ws()
	return s.find(segs)
}

type jscan struct {
	b string
	i int
}

func (s *jscan) ws() {
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case ' ', '\t', '\n', '\r':
			s.i++
		default:
			return
		}
	}
}

func (s *jscan) errf(format string, a ...any) error {
	return fmt.Errorf("invalid JSON at offset %d: %s", s.i, fmt.Sprintf(format, a...))
}

func (s *jscan) expect(ch byte) error {
	s.ws()
	if s.i >= len(s.b) || s.b[s.i] != ch {
		return s.errf("expected %q", ch)
	}
	s.i++
	return nil
}

func (s *jscan) str() (string, error) {
	start := s.i
	if s.i >= len(s.b) || s.b[s.i] != '"' {
		return "", s.errf("expected string")
	}
	s.i++
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case '\\':
			s.i += 2
			continue
		case '"':
			s.i++
			var out string
			if err := json.Unmarshal([]byte(s.b[start:s.i]), &out); err != nil {
				return "", s.errf("bad string")
			}
			return out, nil
		}
		s.i++
	}
	return "", s.errf("unterminated string")
}

func (s *jscan) skip() error {
	s.ws()
	if s.i >= len(s.b) {
		return s.errf("unexpected end")
	}
	switch s.b[s.i] {
	case '"':
		_, err := s.str()
		return err
	case '{', '[':
		depth := 0
		for s.i < len(s.b) {
			switch s.b[s.i] {
			case '"':
				if _, err := s.str(); err != nil {
					return err
				}
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					s.i++
					return nil
				}
			}
			s.i++
		}
		return s.errf("unterminated container")
	default:
		for s.i < len(s.b) {
			switch s.b[s.i] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				return nil
			}
			s.i++
		}
		return nil
	}
}

func (s *jscan) find(segs []Segment) (int, int, error) {
	s.ws()
	if len(segs) == 0 {
		start := s.i
		if err := s.skip(); err != nil {
			return 0, 0, err
		}
		return start, s.i, nil
	}
	seg := segs[0]
	if s.i >= len(s.b) {
		return 0, 0, s.errf("unexpected end")
	}
	if seg.IsIdx {
		if s.b[s.i] != '[' {
			return 0, 0, errNotFound
		}
		s.i++
		for idx := 0; ; idx++ {
			s.ws()
			if s.i < len(s.b) && s.b[s.i] == ']' {
				return 0, 0, errNotFound
			}
			if idx == seg.Index {
				return s.find(segs[1:])
			}
			if err := s.skip(); err != nil {
				return 0, 0, err
			}
			s.ws()
			if s.i < len(s.b) && s.b[s.i] == ',' {
				s.i++
			}
		}
	}
	if s.b[s.i] != '{' {
		return 0, 0, errNotFound
	}
	s.i++
	for {
		s.ws()
		if s.i < len(s.b) && s.b[s.i] == '}' {
			return 0, 0, errNotFound
		}
		key, err := s.str()
		if err != nil {
			return 0, 0, err
		}
		if err := s.expect(':'); err != nil {
			return 0, 0, err
		}
		if key == seg.Key {
			return s.find(segs[1:])
		}
		if err := s.skip(); err != nil {
			return 0, 0, err
		}
		s.ws()
		if s.i < len(s.b) && s.b[s.i] == ',' {
			s.i++
		}
	}
}
