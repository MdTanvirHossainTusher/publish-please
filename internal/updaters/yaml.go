package updaters

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// YAMLField sets the scalar at path (e.g. "$.version") to value, preserving
// the original quoting style, comments and layout.
func YAMLField(path, value string) Updater {
	return Func(func(c string, _ bool) (string, error) {
		segs, err := ParsePath(path)
		if err != nil {
			return c, err
		}
		return SetYAML(c, segs, value)
	})
}

// SetYAML replaces the scalar at segs.
func SetYAML(c string, segs []Segment, value string) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(c), &doc); err != nil {
		return c, fmt.Errorf("yaml: %w", err)
	}
	if len(doc.Content) == 0 {
		return c, fmt.Errorf("yaml %s: empty document", pathString(segs))
	}
	n := doc.Content[0]
	for _, s := range segs {
		n = yamlChild(n, s)
		if n == nil {
			return c, fmt.Errorf("yaml %s: %w", pathString(segs), errNotFound)
		}
	}
	if n.Kind != yaml.ScalarNode {
		return c, fmt.Errorf("yaml %s: not a scalar", pathString(segs))
	}
	off := offsetOf(c, n.Line, n.Column)
	if off < 0 {
		return c, fmt.Errorf("yaml %s: cannot locate value", pathString(segs))
	}
	switch n.Style {
	case yaml.DoubleQuotedStyle, yaml.SingleQuotedStyle:
		q := c[off]
		end := strings.IndexByte(c[off+1:], q)
		if end < 0 {
			return c, fmt.Errorf("yaml %s: unterminated quote", pathString(segs))
		}
		return splice(c, off+1, off+1+end, value), nil
	default:
		if !strings.HasPrefix(c[off:], n.Value) {
			return c, fmt.Errorf("yaml %s: unsupported scalar style", pathString(segs))
		}
		return splice(c, off, off+len(n.Value), value), nil
	}
}

func yamlChild(n *yaml.Node, s Segment) *yaml.Node {
	for n.Kind == yaml.DocumentNode || n.Kind == yaml.AliasNode {
		if n.Kind == yaml.AliasNode {
			n = n.Alias
		} else {
			n = n.Content[0]
		}
	}
	if s.IsIdx {
		if n.Kind != yaml.SequenceNode || s.Index >= len(n.Content) {
			return nil
		}
		return n.Content[s.Index]
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == s.Key {
			return n.Content[i+1]
		}
	}
	return nil
}

// offsetOf converts a 1-based line and character column into a byte offset.
func offsetOf(c string, line, col int) int {
	off := 0
	for l := 1; l < line; l++ {
		i := strings.IndexByte(c[off:], '\n')
		if i < 0 {
			return -1
		}
		off += i + 1
	}
	for k := 1; k < col; k++ {
		if off >= len(c) {
			return -1
		}
		_, w := utf8.DecodeRuneInString(c[off:])
		off += w
	}
	return off
}
