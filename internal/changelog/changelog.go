// Package changelog renders release notes and updates CHANGELOG.md files.
package changelog

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/MdTanvirHossainTusher/publish-please/internal/commits"
	"github.com/MdTanvirHossainTusher/publish-please/internal/config"
	"github.com/MdTanvirHossainTusher/publish-please/internal/updaters"
)

// Links builds forge URLs. Any function may be nil, which disables that link.
type Links struct {
	Commit  func(sha string) string
	Compare func(from, to string) string
	Issue   func(n int) string
	// IssuePrefix is "#" for GitHub/Gitea and "!" for GitLab merge requests.
	IssuePrefix string
}

// Input is everything needed to render one release's notes.
type Input struct {
	Version     string
	PreviousTag string
	CurrentTag  string
	Date        time.Time
	DateFormat  string // Go layout; default 2006-01-02
	Changes     []commits.Change
	Sections    []config.Section
	Links       Links
	// SentenceCase capitalises the first letter of each entry (sentence-case plugin).
	SentenceCase bool
}

// Releasable reports whether changes contain anything that belongs in a
// visible changelog section, or a breaking change, or a Release-As pin.
func Releasable(changes []commits.Change, sections []config.Section) bool {
	visible := map[string]bool{}
	for _, s := range sections {
		if !s.Hidden {
			visible[s.Type] = true
		}
	}
	for _, c := range changes {
		if c.Breaking || c.ReleaseAs != "" || visible[c.Type] {
			return true
		}
	}
	return false
}

// Render builds the markdown notes for a release, starting with the "## " heading.
func Render(in Input) string {
	var b strings.Builder
	layout := in.DateFormat
	if layout == "" {
		layout = "2006-01-02"
	}
	date := in.Date.Format(convertDateFormat(layout))
	if in.PreviousTag != "" && in.Links.Compare != nil {
		fmt.Fprintf(&b, "## [%s](%s) (%s)\n", in.Version, in.Links.Compare(in.PreviousTag, in.CurrentTag), date)
	} else {
		fmt.Fprintf(&b, "## %s (%s)\n", in.Version, date)
	}

	var breaking []string
	for _, c := range in.Changes {
		if c.Breaking {
			breaking = appendUnique(breaking, entry(in, c, c.BreakingNote))
		}
	}
	if len(breaking) > 0 {
		b.WriteString("\n\n### ⚠ BREAKING CHANGES\n\n")
		b.WriteString(strings.Join(breaking, "\n"))
		b.WriteString("\n")
	}

	order := []string{}
	titles := map[string]string{}
	hidden := map[string]bool{}
	for _, s := range in.Sections {
		if _, seen := titles[s.Type]; !seen {
			titles[s.Type] = s.Section
			hidden[s.Type] = s.Hidden
		}
		if !s.Hidden && !contains(order, s.Section) {
			order = append(order, s.Section)
		}
	}
	bySection := map[string][]string{}
	for _, c := range in.Changes {
		title, ok := titles[c.Type]
		if !ok || hidden[c.Type] {
			continue
		}
		bySection[title] = appendUnique(bySection[title], entry(in, c, c.Subject))
	}
	for _, title := range order {
		lines := bySection[title]
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n\n### %s\n\n", title)
		b.WriteString(strings.Join(lines, "\n"))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

var issueRe = regexp.MustCompile(`(^|[\s(])([#!])(\d+)\b`)

func entry(in Input, c commits.Change, text string) string {
	text = strings.TrimSpace(text)
	if in.SentenceCase {
		text = sentenceCase(text)
	}
	// Multi-line notes are indented under the bullet.
	text = strings.ReplaceAll(text, "\n", "\n  ")
	if in.Links.Issue != nil {
		prefix := in.Links.IssuePrefix
		if prefix == "" {
			prefix = "#"
		}
		text = issueRe.ReplaceAllStringFunc(text, func(m string) string {
			sm := issueRe.FindStringSubmatch(m)
			if sm[2] != prefix {
				return m
			}
			var n int
			fmt.Sscan(sm[3], &n)
			return fmt.Sprintf("%s[%s%s](%s)", sm[1], sm[2], sm[3], in.Links.Issue(n))
		})
	}
	line := "* "
	if c.Scope != "" {
		line += "**" + c.Scope + ":** "
	}
	line += text
	if c.SHA != "" {
		short := c.SHA
		if len(short) > 7 {
			short = short[:7]
		}
		if in.Links.Commit != nil {
			line += fmt.Sprintf(" ([%s](%s))", short, in.Links.Commit(c.SHA))
		} else {
			line += " (" + short + ")"
		}
	}
	return line
}

func sentenceCase(s string) string {
	r, w := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[w:]
}

func appendUnique(xs []string, s string) []string {
	if contains(xs, s) {
		return xs
	}
	return append(xs, s)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// convertDateFormat accepts either a Go layout or a strftime-like format
// (release-please's "date-format", e.g. "%Y-%m-%d").
func convertDateFormat(f string) string {
	if !strings.Contains(f, "%") {
		return f
	}
	r := strings.NewReplacer("%Y", "2006", "%m", "01", "%d", "02", "%B", "January", "%b", "Jan", "%y", "06", "%e", "_2", "%H", "15", "%M", "04", "%S", "05")
	return r.Replace(f)
}

const header = "# Changelog\n"

// Prepend returns an updater that inserts notes above the newest entry.
func Prepend(notes string) updaters.Updater {
	return updaters.Func(func(c string, exists bool) (string, error) {
		notes = strings.TrimRight(notes, "\n") + "\n"
		if !exists || strings.TrimSpace(c) == "" {
			return header + "\n" + notes, nil
		}
		lines := strings.SplitAfter(c, "\n")
		for i, l := range lines {
			if strings.HasPrefix(l, "## ") || looksLikeVersionHeading(l) {
				before := strings.Join(lines[:i], "")
				after := strings.Join(lines[i:], "")
				return before + notes + "\n" + after, nil
			}
		}
		// No previous entries: put the notes after the title (or at the top).
		if strings.HasPrefix(c, "# ") {
			nl := strings.IndexByte(c, '\n')
			if nl < 0 {
				return c + "\n\n" + notes, nil
			}
			rest := strings.TrimLeft(c[nl+1:], "\n")
			out := c[:nl+1] + "\n" + notes
			if rest != "" {
				out += "\n" + rest
			}
			return out, nil
		}
		return header + "\n" + notes + "\n" + c, nil
	})
}

var versionHeadingRe = regexp.MustCompile(`^###? \[?v?\d+\.\d+\.\d+`)

func looksLikeVersionHeading(l string) bool { return versionHeadingRe.MatchString(l) }
