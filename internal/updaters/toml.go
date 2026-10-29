package updaters

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	tomlTableRe    = regexp.MustCompile(`^\s*\[\s*([^\[\]]+?)\s*\]\s*(?:#.*)?$`)
	tomlArrayTblRe = regexp.MustCompile(`^\s*\[\[\s*([^\[\]]+?)\s*\]\]\s*(?:#.*)?$`)
	tomlKeyRe      = regexp.MustCompile(`^\s*([A-Za-z0-9_."'-]+?)\s*=\s*(.*)$`)
)

// TOMLField sets the string at path (e.g. "$.package.version"). Arrays of
// tables are addressed by index: "$.package[2].version".
func TOMLField(path, value string) Updater {
	return Func(func(c string, _ bool) (string, error) {
		segs, err := ParsePath(path)
		if err != nil {
			return c, err
		}
		return SetTOML(c, segs, value)
	})
}

// SetTOML replaces a string value in a TOML document.
func SetTOML(c string, segs []Segment, value string) (string, error) {
	want := tomlKey(segs)
	return tomlEach(c, func(full string, valStart, valEnd int) (string, bool) {
		if full == want {
			return value, true
		}
		return "", false
	}, true, pathString(segs))
}

func tomlKey(segs []Segment) string {
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = s.String()
	}
	return strings.Join(parts, ".")
}

// tomlEach walks key/value lines whose value is a basic or literal string,
// calling fn with the full dotted key ("table.key", "array[1].key").
// Returning (new, true) replaces the string's contents.
func tomlEach(c string, fn func(full string, s, e int) (string, bool), firstOnly bool, label string) (string, error) {
	lines := strings.SplitAfter(c, "\n")
	table := ""
	arrayCount := map[string]int{}
	found := false
	inMultiline := false
	for i, l := range lines {
		if inMultiline {
			if strings.Contains(l, `"""`) || strings.Contains(l, `'''`) {
				inMultiline = false
			}
			continue
		}
		body := strings.TrimRight(l, "\r\n")
		if m := tomlArrayTblRe.FindStringSubmatch(body); m != nil {
			name := unquoteKey(m[1])
			table = fmt.Sprintf("%s[%d]", name, arrayCount[name])
			arrayCount[name]++
			continue
		}
		if m := tomlTableRe.FindStringSubmatch(body); m != nil {
			table = unquoteKey(m[1])
			continue
		}
		m := tomlKeyRe.FindStringSubmatchIndex(body)
		if m == nil {
			continue
		}
		key := unquoteKey(body[m[2]:m[3]])
		val := body[m[4]:m[5]]
		if strings.HasPrefix(val, `"""`) || strings.HasPrefix(val, `'''`) {
			if strings.Count(val, val[:3]) < 2 {
				inMultiline = true
			}
			continue
		}
		if len(val) == 0 || (val[0] != '"' && val[0] != '\'') {
			continue
		}
		end := strings.IndexByte(val[1:], val[0])
		if end < 0 {
			continue
		}
		full := key
		if table != "" {
			full = table + "." + key
		}
		vs := m[4] + 1
		ve := vs + end
		if nv, ok := fn(full, vs, ve); ok {
			lines[i] = body[:vs] + nv + body[ve:] + l[len(body):]
			found = true
			if firstOnly {
				break
			}
		}
	}
	if !found && firstOnly {
		return c, fmt.Errorf("toml %s: %w", label, errNotFound)
	}
	return strings.Join(lines, ""), nil
}

func unquoteKey(k string) string {
	parts := strings.Split(k, ".")
	for i, p := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(p), `"'`)
	}
	return strings.Join(parts, ".")
}

// CargoLock updates the version of the named packages in Cargo.lock.
func CargoLock(versions map[string]string) Updater {
	return Func(func(c string, _ bool) (string, error) {
		lines := strings.SplitAfter(c, "\n")
		inPkg := false
		name := ""
		for i, l := range lines {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, "[") {
				inPkg = t == "[[package]]"
				name = ""
				continue
			}
			if !inPkg {
				continue
			}
			if m := tomlKeyRe.FindStringSubmatch(strings.TrimRight(l, "\r\n")); m != nil {
				val := strings.Trim(strings.TrimSpace(m[2]), `"`)
				switch m[1] {
				case "name":
					name = val
				case "version":
					if v, ok := versions[name]; ok && name != "" {
						lines[i] = strings.Replace(l, `"`+val+`"`, `"`+v+`"`, 1)
					}
				}
			}
		}
		return strings.Join(lines, ""), nil
	})
}
