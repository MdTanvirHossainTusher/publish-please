package changelog

import (
	"fmt"
	"testing"
	"time"

	"github.com/MdTanvirHossainTusher/publish-please/internal/commits"
	"github.com/MdTanvirHossainTusher/publish-please/internal/config"
)

var links = Links{
	Commit:  func(s string) string { return "https://git.example.com/o/r/commit/" + s },
	Compare: func(a, b string) string { return "https://git.example.com/o/r/compare/" + a + "..." + b },
	Issue:   func(n int) string { return fmt.Sprintf("https://git.example.com/o/r/issues/%d", n) },
}

func TestRender(t *testing.T) {
	got := Render(Input{
		Version:     "1.1.0",
		PreviousTag: "v1.0.0",
		CurrentTag:  "v1.1.0",
		Date:        time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC),
		Sections:    config.DefaultSections,
		Links:       links,
		Changes: []commits.Change{
			{Type: "feat", Scope: "api", Subject: "add users (#12)", SHA: "abcdef1234"},
			{Type: "fix", Subject: "crash on start", SHA: "1234567890", Breaking: true, BreakingNote: "config moved"},
			{Type: "chore", Subject: "hidden", SHA: "ffffffffff"},
		},
	})
	want := `## [1.1.0](https://git.example.com/o/r/compare/v1.0.0...v1.1.0) (2026-10-29)


### ⚠ BREAKING CHANGES

* config moved ([1234567](https://git.example.com/o/r/commit/1234567890))


### Features

* **api:** add users ([#12](https://git.example.com/o/r/issues/12)) ([abcdef1](https://git.example.com/o/r/commit/abcdef1234))


### Bug Fixes

* crash on start ([1234567](https://git.example.com/o/r/commit/1234567890))
`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestReleasable(t *testing.T) {
	if Releasable([]commits.Change{{Type: "chore"}, {Type: "docs"}}, config.DefaultSections) {
		t.Fatal("hidden only should not release")
	}
	if !Releasable([]commits.Change{{Type: "chore", Breaking: true}}, config.DefaultSections) {
		t.Fatal("breaking should release")
	}
}

func TestPrepend(t *testing.T) {
	u := Prepend("## 1.1.0 (2026-10-29)\n\n* new\n")
	got, _ := u.Update("", false)
	if got != "# Changelog\n\n## 1.1.0 (2026-10-29)\n\n* new\n" {
		t.Fatalf("new file: %q", got)
	}
	got, _ = u.Update("# Changelog\n\nIntro text.\n\n## 1.0.0 (2026-01-01)\n\n* old\n", true)
	if got != "# Changelog\n\nIntro text.\n\n## 1.1.0 (2026-10-29)\n\n* new\n\n## 1.0.0 (2026-01-01)\n\n* old\n" {
		t.Fatalf("existing: %q", got)
	}
	got, _ = u.Update("# Changelog\n", true)
	if got != "# Changelog\n\n## 1.1.0 (2026-10-29)\n\n* new\n" {
		t.Fatalf("header only: %q", got)
	}
}

func TestDateFormat(t *testing.T) {
	got := Render(Input{Version: "1.0.0", Date: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), DateFormat: "%d %B %Y", Sections: config.DefaultSections})
	if got != "## 1.0.0 (02 January 2026)\n" {
		t.Fatalf("got %q", got)
	}
}
