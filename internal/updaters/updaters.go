// Package updaters rewrites version strings inside files.
//
// Every structured updater (JSON, YAML, TOML, XML) locates the exact byte
// span of the value and splices the new text in, so formatting, comments
// and key order are preserved.
package updaters

import (
	"fmt"
	"regexp"
	"strings"
)

// Updater transforms a file's content. exists is false when the file is new.
type Updater interface {
	Update(content string, exists bool) (string, error)
}

// Func adapts a function into an Updater.
type Func func(content string, exists bool) (string, error)

func (f Func) Update(c string, e bool) (string, error) { return f(c, e) }

// FileUpdate pairs a repository path with an updater.
type FileUpdate struct {
	Path string
	// Create allows the file to be created when missing. Otherwise a
	// missing file is skipped silently (release-please behaviour).
	Create bool
	// Optional updates are skipped (not failed) when the value is not found.
	Optional bool
	Updater  Updater
}

// SemverRe matches a semantic version embedded in text.
var SemverRe = regexp.MustCompile(`\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?`)

// splice replaces b[start:end] with s.
func splice(b string, start, end int, s string) string {
	return b[:start] + s + b[end:]
}

// VersionTxt replaces the whole file with the version (e.g. version.txt).
func VersionTxt(version string) Updater {
	return Func(func(string, bool) (string, error) { return version + "\n", nil })
}

// Regex replaces capture group 1 of the first match of pattern.
func Regex(pattern, version string) Updater {
	re := regexp.MustCompile(pattern)
	return Func(func(c string, _ bool) (string, error) {
		m := re.FindStringSubmatchIndex(c)
		if len(m) < 4 || m[2] < 0 {
			return c, fmt.Errorf("pattern %q not found", pattern)
		}
		return splice(c, m[2], m[3], version), nil
	})
}

// RegexAll replaces capture group 1 of every match of pattern. Missing is not an error.
func RegexAll(pattern, version string) Updater {
	re := regexp.MustCompile(pattern)
	return Func(func(c string, _ bool) (string, error) {
		ms := re.FindAllStringSubmatchIndex(c, -1)
		for i := len(ms) - 1; i >= 0; i-- {
			if ms[i][2] >= 0 {
				c = splice(c, ms[i][2], ms[i][3], version)
			}
		}
		return c, nil
	})
}

var (
	markerLineRe  = regexp.MustCompile(`x-(?:release|publish)-please-(version|major|minor|patch)\b`)
	markerStartRe = regexp.MustCompile(`x-(?:release|publish)-please-start-(version|major|minor|patch)\b`)
	markerEndRe   = regexp.MustCompile(`x-(?:release|publish)-please-end\b`)
	numberRe      = regexp.MustCompile(`\d+`)
)

// Generic updates lines annotated with x-release-please-* markers (the
// x-publish-please-* spelling also works):
//
//	version = "1.2.3" // x-release-please-version
//	x-release-please-start-version ... x-release-please-end
//
// major/minor/patch markers replace the first integer with that component.
func Generic(major, minor, patch int, version string) Updater {
	part := func(kind string) string {
		switch kind {
		case "major":
			return fmt.Sprint(major)
		case "minor":
			return fmt.Sprint(minor)
		case "patch":
			return fmt.Sprint(patch)
		}
		return version
	}
	apply := func(line, kind string, all bool) string {
		re := SemverRe
		if kind != "version" {
			re = numberRe
		}
		if all {
			return re.ReplaceAllLiteralString(line, part(kind))
		}
		done := false
		return re.ReplaceAllStringFunc(line, func(s string) string {
			if done {
				return s
			}
			done = true
			return part(kind)
		})
	}
	return Func(func(c string, _ bool) (string, error) {
		lines := strings.SplitAfter(c, "\n")
		block := ""
		for i, l := range lines {
			if m := markerStartRe.FindStringSubmatch(l); m != nil {
				block = m[1]
				continue
			}
			if markerEndRe.MatchString(l) {
				block = ""
				continue
			}
			if block != "" {
				lines[i] = apply(l, block, true)
				continue
			}
			if m := markerLineRe.FindStringSubmatchIndex(l); m != nil {
				kind := l[m[2]:m[3]]
				// only touch the text before the marker comment
				lines[i] = apply(l[:m[0]], kind, false) + l[m[0]:]
			}
		}
		return strings.Join(lines, ""), nil
	})
}
