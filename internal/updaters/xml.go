package updaters

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// XMLField sets the text of the element at xpath (e.g. "/project/version").
// Only absolute element paths are supported; namespaces are ignored.
// A leading "//" matches the path at any depth.
func XMLField(xpath, value string) Updater {
	return Func(func(c string, _ bool) (string, error) {
		return SetXML(c, xpath, value)
	})
}

// SetXML replaces the text of the first element matching xpath.
func SetXML(c, xpath, value string) (string, error) {
	anywhere := strings.HasPrefix(xpath, "//")
	want := strings.Split(strings.Trim(xpath, "/"), "/")
	dec := xml.NewDecoder(strings.NewReader(c))
	dec.Strict = false
	var stack []string
	matched := func() bool {
		if anywhere {
			if len(stack) < len(want) {
				return false
			}
			return equal(stack[len(stack)-len(want):], want)
		}
		return equal(stack, want)
	}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return c, fmt.Errorf("xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			if matched() {
				// element may be empty: <version/> or <version></version>
				start := int(dec.InputOffset())
				next, err := dec.Token()
				if err != nil {
					return c, fmt.Errorf("xml: %w", err)
				}
				switch next.(type) {
				case xml.CharData:
					end := int(dec.InputOffset())
					text := c[start:end]
					lead := len(text) - len(strings.TrimLeft(text, " \t\r\n"))
					trail := len(text) - len(strings.TrimRight(text, " \t\r\n"))
					return splice(c, start+lead, end-trail, value), nil
				case xml.EndElement:
					if c[start-2:start] == "/>" {
						return c, fmt.Errorf("xml %s: empty element", xpath)
					}
					return splice(c, start, start, value), nil
				default:
					return c, fmt.Errorf("xml %s: element has children", xpath)
				}
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return c, fmt.Errorf("xml %s: %w", xpath, errNotFound)
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
