package commits

import "testing"

func TestParseSimple(t *testing.T) {
	cs := ParseMessage("feat(api): add users endpoint (#12)")
	if len(cs) != 1 {
		t.Fatalf("got %d changes", len(cs))
	}
	c := cs[0]
	if c.Type != "feat" || c.Scope != "api" || c.Subject != "add users endpoint (#12)" || c.PR != 12 || c.Breaking {
		t.Fatalf("unexpected %+v", c)
	}
}

func TestParseNonConventional(t *testing.T) {
	if cs := ParseMessage("Update README"); len(cs) != 0 {
		t.Fatalf("expected none, got %+v", cs)
	}
}

func TestParseBreaking(t *testing.T) {
	cs := ParseMessage("refactor!: drop node 14")
	if !cs[0].Breaking || cs[0].BreakingNote != "drop node 14" {
		t.Fatalf("unexpected %+v", cs[0])
	}
	cs = ParseMessage("fix: x\n\nsome body\n\nBREAKING CHANGE: the config moved\nto a new file\n\nRefs: #4")
	if !cs[0].Breaking || cs[0].BreakingNote != "the config moved\nto a new file" {
		t.Fatalf("unexpected note %q", cs[0].BreakingNote)
	}
}

func TestParseReleaseAs(t *testing.T) {
	cs := ParseMessage("chore: release 2.0.0\n\nRelease-As: v2.0.0")
	if cs[0].ReleaseAs != "2.0.0" {
		t.Fatalf("got %q", cs[0].ReleaseAs)
	}
}

func TestParseMultipleChanges(t *testing.T) {
	msg := `feat: adds v4 UUID to crypto

This adds support for v4 UUIDs to the library.

fix(utils): unicode no longer throws exception
  PiperOrigin-RevId: 345559154
  BREAKING-CHANGE: encode method no longer throws.
  Source-Link: googleapis/googleapis@5e0dcb2

feat(utils): update encode to support unicode
  PiperOrigin-RevId: 345559182`
	cs := ParseMessage(msg)
	if len(cs) != 3 {
		t.Fatalf("got %d: %+v", len(cs), cs)
	}
	if cs[1].Type != "fix" || cs[1].Scope != "utils" || !cs[1].Breaking || cs[1].BreakingNote != "encode method no longer throws." {
		t.Fatalf("unexpected second %+v", cs[1])
	}
	if cs[2].Type != "feat" || cs[2].Breaking {
		t.Fatalf("unexpected third %+v", cs[2])
	}
	if cs[0].Breaking {
		t.Fatal("first must not inherit breaking")
	}
}

func TestOverride(t *testing.T) {
	body := "Some PR\r\n\r\nBEGIN_COMMIT_OVERRIDE\r\nfeat: a\r\nfix(x): b\r\nEND_COMMIT_OVERRIDE\r\n"
	text, ok := Override(body)
	if !ok {
		t.Fatal("no override")
	}
	cs := ParseOverride(text)
	if len(cs) != 2 || cs[0].Type != "feat" || cs[1].Scope != "x" {
		t.Fatalf("unexpected %+v", cs)
	}
}

func TestMergedPR(t *testing.T) {
	cases := map[string]int{
		"feat: x (#42)": 42,
		"Merge pull request #7 from a/b\n\nfeat: y":                 7,
		"Merge branch 'x' into 'main'\n\nSee merge request grp/p!9": 9,
		"feat: nothing": 0,
	}
	for msg, want := range cases {
		if got := MergedPR(msg); got != want {
			t.Errorf("%q: got %d want %d", msg, got, want)
		}
	}
}
