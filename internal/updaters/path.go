package updaters

import (
	"fmt"
	"strconv"
	"strings"
)

// Segment is one step of a JSONPath: a key or an array index.
type Segment struct {
	Key   string
	Index int
	IsIdx bool
}

func (s Segment) String() string {
	if s.IsIdx {
		return fmt.Sprintf("[%d]", s.Index)
	}
	return s.Key
}

// Keys builds a path from plain keys (useful when keys contain dots or "@").
func Keys(keys ...string) []Segment {
	out := make([]Segment, len(keys))
	for i, k := range keys {
		out[i] = Segment{Key: k}
	}
	return out
}

// ParsePath parses the JSONPath subset used by release-please configs:
// $.a.b, $.a[0].b, $['a']["b"], $.packages[""].version
func ParsePath(p string) ([]Segment, error) {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "$")
	var out []Segment
	for i := 0; i < len(p); {
		switch p[i] {
		case '.':
			i++
			j := i
			for j < len(p) && p[j] != '.' && p[j] != '[' {
				j++
			}
			if j == i {
				return nil, fmt.Errorf("jsonpath %q: empty key", p)
			}
			out = append(out, Segment{Key: p[i:j]})
			i = j
		case '[':
			end := strings.IndexByte(p[i:], ']')
			if end < 0 {
				return nil, fmt.Errorf("jsonpath %q: unclosed [", p)
			}
			inner := p[i+1 : i+end]
			if len(inner) >= 2 && (inner[0] == '\'' || inner[0] == '"') && inner[len(inner)-1] == inner[0] {
				out = append(out, Segment{Key: inner[1 : len(inner)-1]})
			} else {
				n, err := strconv.Atoi(inner)
				if err != nil {
					return nil, fmt.Errorf("jsonpath %q: bad index %q", p, inner)
				}
				out = append(out, Segment{Index: n, IsIdx: true})
			}
			i += end + 1
		default:
			// allow a leading bare key: "version"
			j := i
			for j < len(p) && p[j] != '.' && p[j] != '[' {
				j++
			}
			out = append(out, Segment{Key: p[i:j]})
			i = j
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("jsonpath %q: empty", p)
	}
	return out, nil
}

func pathString(ps []Segment) string {
	var b strings.Builder
	b.WriteString("$")
	for _, s := range ps {
		if s.IsIdx {
			b.WriteString(s.String())
		} else {
			b.WriteString("." + s.Key)
		}
	}
	return b.String()
}
