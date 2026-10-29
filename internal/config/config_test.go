package config

import "testing"

func TestParseDefaultsAndOverrides(t *testing.T) {
	m, err := Parse([]byte(`{
  "$schema": "x",
  "release-type": "node",
  "bump-minor-pre-major": true,
  "label": "release: pending, extra",
  "separate-pull-requests": true,
  "plugins": ["node-workspace", {"type": "linked-versions", "groupName": "g", "components": ["a", "b"]}],
  "extra-files": ["a.txt", {"type": "json", "path": "b.json", "jsonpath": "$.v"}],
  "packages": {
    ".": {},
    "./packages/a/": {"release-type": "simple", "bump-minor-pre-major": false, "include-v-in-tag": false}
  }
}`))
	if err != nil {
		t.Fatal(err)
	}
	root, a := m.Packages["."], m.Packages["packages/a"]
	if root == nil || a == nil {
		t.Fatalf("paths not normalised: %v", m.Paths())
	}
	if root.ReleaseType != "node" || !root.BumpMinorPreMajor {
		t.Fatalf("root did not inherit defaults: %+v", root)
	}
	if a.ReleaseType != "simple" || a.BumpMinorPreMajor || a.IncludeVInTag == nil || *a.IncludeVInTag {
		t.Fatalf("package override failed: %+v", a)
	}
	if len(a.ExtraFiles) != 2 || a.ExtraFiles[0].Type != "generic" || a.ExtraFiles[1].JSONPath != "$.v" {
		t.Fatalf("extra-files: %+v", a.ExtraFiles)
	}
	if len(m.Labels) != 2 || m.Labels[0] != "release: pending" || m.ReleaseLabels[0] != DefaultReleaseLabel {
		t.Fatalf("labels %v %v", m.Labels, m.ReleaseLabels)
	}
	if !m.SeparatePullRequests || len(m.Plugins) != 2 || m.Plugins[0].Type != "node-workspace" || m.Plugins[1].Components[1] != "b" {
		t.Fatalf("manifest options %+v", m)
	}
	if got := m.Paths(); got[0] != "." || got[1] != "packages/a" {
		t.Fatalf("order %v", got)
	}
	if a.ChangelogFile() != "packages/a/CHANGELOG.md" || root.ChangelogFile() != "CHANGELOG.md" {
		t.Fatal("changelog paths")
	}
}

func TestJoin(t *testing.T) {
	cases := [][3]string{
		{".", "a.txt", "a.txt"},
		{"pkg", "a.txt", "pkg/a.txt"},
		{"pkg", "/root.txt", "root.txt"},
		{"pkg", "../x", "x"},
	}
	for _, c := range cases {
		if got := Join(c[0], c[1]); got != c[2] {
			t.Errorf("Join(%q,%q)=%q want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := Parse([]byte(`{"packages": {}}`)); err == nil {
		t.Fatal("expected error for no packages")
	}
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Fatal("expected error for bad json")
	}
	v, err := ParseVersions([]byte(`{"./a": "1.0.0", ".": "2.0.0"}`))
	if err != nil || v["a"] != "1.0.0" || v["."] != "2.0.0" {
		t.Fatalf("versions %v %v", v, err)
	}
}
