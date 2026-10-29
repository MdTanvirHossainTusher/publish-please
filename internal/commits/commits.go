// Package commits parses Conventional Commits (https://www.conventionalcommits.org).
package commits

import (
	"regexp"
	"strconv"
	"strings"
)

// Commit is a raw commit read from git history.
type Commit struct {
	SHA     string
	Message string
	Files   []string
}

// Change is one conventional change. A single commit can hold several.
type Change struct {
	Type         string
	Scope        string
	Subject      string
	Breaking     bool
	BreakingNote string
	ReleaseAs    string
	SHA          string
	// PR is the pull/merge request number found in the subject, e.g. "(#12)" or "(!12)". 0 if none.
	PR int
	// Files are the files touched by the source commit.
	Files []string
}

var (
	headerRe    = regexp.MustCompile(`^([A-Za-z][\w-]*)(?:\(([^()\r\n]*)\))?(!)?:[ \t]+(.+)$`)
	footerRe    = regexp.MustCompile(`^(BREAKING[ -]CHANGE|[A-Za-z][\w-]*)(?::[ \t]+|[ \t]+#)(.*)$`)
	prRefRe     = regexp.MustCompile(`\((?:#|!)(\d+)\)\s*$`)
	overrideRe  = regexp.MustCompile(`(?s)BEGIN_COMMIT_OVERRIDE\s*\n(.*?)\n\s*END_COMMIT_OVERRIDE`)
	mergeRefRes = []*regexp.Regexp{
		regexp.MustCompile(`\((?:#|!)(\d+)\)\s*$`),            // squash: "subject (#12)"
		regexp.MustCompile(`^Merge pull request #(\d+)\b`),    // GitHub/Gitea merge commit
		regexp.MustCompile(`See merge request [^\s!]*!(\d+)`), // GitLab merge commit
	}
)

// Parse turns a commit into zero or more conventional changes.
// Commits that are not conventional yield no changes.
func Parse(c Commit) []Change {
	changes := ParseMessage(c.Message)
	for i := range changes {
		changes[i].SHA = c.SHA
		changes[i].Files = c.Files
	}
	return changes
}

// ParseMessage parses a commit message. Besides the header, additional
// conventional headers in the footer area each start a new change, with
// their own indented footers (release-please's "multiple changes" format).
func ParseMessage(msg string) []Change {
	msg = strings.ReplaceAll(msg, "\r\n", "\n")
	lines := strings.Split(strings.TrimSpace(msg), "\n")
	if len(lines) == 0 {
		return nil
	}
	first, ok := parseHeader(lines[0])
	if !ok {
		return nil
	}
	changes := []*Change{&first}
	cur := &first
	var lastFooter string
	for _, raw := range lines[1:] {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimLeft(line, " \t")
		indented := len(trimmed) < len(line)

		if !indented && trimmed != "" {
			if ch, ok := parseHeader(trimmed); ok && !isFooterKey(ch.Type) {
				c := ch
				changes = append(changes, &c)
				cur = &c
				lastFooter = ""
				continue
			}
		}
		if trimmed == "" {
			if lastFooter == "BREAKING CHANGE" {
				cur.BreakingNote += "\n"
			}
			continue
		}
		if m := footerRe.FindStringSubmatch(trimmed); m != nil && (indented || cur == &first) {
			key := normaliseFooterKey(m[1])
			val := strings.TrimSpace(m[2])
			switch key {
			case "BREAKING CHANGE":
				cur.Breaking = true
				cur.BreakingNote = val
			case "RELEASE-AS":
				cur.ReleaseAs = strings.TrimPrefix(val, "v")
			}
			lastFooter = key
			continue
		}
		if lastFooter == "BREAKING CHANGE" {
			cur.BreakingNote += "\n" + trimmed
		}
	}
	out := make([]Change, 0, len(changes))
	for _, c := range changes {
		c.BreakingNote = strings.TrimSpace(c.BreakingNote)
		if c.Breaking && c.BreakingNote == "" {
			c.BreakingNote = c.Subject
		}
		out = append(out, *c)
	}
	return out
}

func parseHeader(line string) (Change, bool) {
	m := headerRe.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return Change{}, false
	}
	c := Change{Type: strings.ToLower(m[1]), Scope: m[2], Subject: strings.TrimSpace(m[4]), Breaking: m[3] == "!"}
	if pm := prRefRe.FindStringSubmatch(c.Subject); pm != nil {
		c.PR, _ = strconv.Atoi(pm[1])
	}
	return c, true
}

func isFooterKey(t string) bool {
	// Git trailers are hyphenated (Signed-off-by, Release-As); commit types are not.
	if strings.Contains(t, "-") {
		return true
	}
	switch strings.ToLower(t) {
	case "refs", "fixes", "closes", "resolves", "see":
		return true
	}
	return false
}

func normaliseFooterKey(k string) string {
	u := strings.ToUpper(k)
	if u == "BREAKING CHANGE" || u == "BREAKING-CHANGE" {
		return "BREAKING CHANGE"
	}
	return u
}

// MergedPR returns the pull/merge request number a commit came from, or 0.
func MergedPR(message string) int {
	lines := strings.SplitN(strings.TrimSpace(message), "\n", 2)
	for _, re := range mergeRefRes {
		target := lines[0]
		if strings.HasPrefix(re.String(), "See") {
			target = message
		}
		if m := re.FindStringSubmatch(target); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
	}
	return 0
}

// Override extracts the BEGIN_COMMIT_OVERRIDE block from a PR body.
func Override(body string) (string, bool) {
	m := overrideRe.FindStringSubmatch(strings.ReplaceAll(body, "\r\n", "\n"))
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// ParseOverride parses an override block, where every non-indented
// conventional header line starts a new change.
func ParseOverride(text string) []Change {
	var out []Change
	var buf []string
	flush := func() {
		if len(buf) > 0 {
			out = append(out, ParseMessage(strings.Join(buf, "\n"))...)
			buf = nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if _, ok := parseHeader(line); ok && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			flush()
		}
		if len(buf) > 0 || strings.TrimSpace(line) != "" {
			buf = append(buf, line)
		}
	}
	flush()
	return out
}
