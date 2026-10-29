package manifest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MdTanvirHossainTusher/publish-please/internal/config"
	"github.com/MdTanvirHossainTusher/publish-please/internal/gitrepo"
	"github.com/MdTanvirHossainTusher/publish-please/internal/semver"
)

type testRepo struct {
	t   *testing.T
	dir string
}

func newTestRepo(t *testing.T) *testRepo {
	r := &testRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	r.git("config", "user.name", "T")
	r.git("config", "user.email", "t@example.com")
	return r
}

func (r *testRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *testRepo) write(files map[string]string) {
	for p, c := range files {
		full := filepath.Join(r.dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
}

func (r *testRepo) commit(msg string, files map[string]string) {
	r.write(files)
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", msg)
}

// runner builds a Runner over the repo with no forge.
func (r *testRepo) runner(cfgJSON string, versions map[string]string) *Runner {
	r.t.Helper()
	cfg, err := config.Parse([]byte(cfgJSON))
	if err != nil {
		r.t.Fatal(err)
	}
	repo, err := gitrepo.Open(r.dir, r.dir, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	ctx := context.Background()
	if err := repo.Fetch(ctx, "main"); err != nil {
		r.t.Fatal(err)
	}
	run := &Runner{
		Repo: repo, Target: "main", Config: cfg, Versions: versions,
		ManifestFile: ".release-please-manifest.json",
		Now:          func() time.Time { return time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC) },
	}
	if err := run.Init(ctx); err != nil {
		r.t.Fatal(err)
	}
	return run
}

func byPath(cs []*Candidate) map[string]*Candidate {
	m := map[string]*Candidate{}
	for _, c := range cs {
		m[c.Pkg.Path] = c
	}
	return m
}

func TestNodeWorkspaceCascade(t *testing.T) {
	r := newTestRepo(t)
	r.commit("chore: init", map[string]string{
		"packages/a/package.json": "{\n  \"name\": \"@x/a\",\n  \"version\": \"1.0.0\"\n}\n",
		"packages/b/package.json": "{\n  \"name\": \"@x/b\",\n  \"version\": \"2.0.0\",\n  \"dependencies\": {\n    \"@x/a\": \"^1.0.0\"\n  }\n}\n",
		"packages/c/package.json": "{\n  \"name\": \"@x/c\",\n  \"version\": \"0.3.0\",\n  \"devDependencies\": {\n    \"@x/b\": \"~2.0.0\",\n    \"@x/a\": \"workspace:*\"\n  }\n}\n",
	})
	r.git("tag", "a-v1.0.0")
	r.git("tag", "b-v2.0.0")
	r.git("tag", "c-v0.3.0")
	r.commit("feat(a): new api", map[string]string{"packages/a/index.js": "x"})

	run := r.runner(`{"release-type":"node","plugins":["node-workspace"],"packages":{
		"packages/a":{}, "packages/b":{}, "packages/c":{}}}`,
		map[string]string{"packages/a": "1.0.0", "packages/b": "2.0.0", "packages/c": "0.3.0"})
	cands, err := run.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := byPath(cands)
	want := map[string]string{"packages/a": "1.1.0", "packages/b": "2.0.1", "packages/c": "0.3.1"}
	if len(m) != 3 {
		t.Fatalf("got %d candidates", len(m))
	}
	for p, v := range want {
		if m[p].Version.String() != v {
			t.Errorf("%s: got %s want %s", p, m[p].Version, v)
		}
	}
	if m["packages/a"].Tag != "a-v1.1.0" {
		t.Errorf("tag %s", m["packages/a"].Tag)
	}
	files, err := run.Render(context.Background(), cands)
	if err != nil {
		t.Fatal(err)
	}
	b := files["packages/b/package.json"]
	if !strings.Contains(b, `"version": "2.0.1"`) || !strings.Contains(b, `"@x/a": "^1.1.0"`) {
		t.Fatalf("b/package.json:\n%s", b)
	}
	c := files["packages/c/package.json"]
	if !strings.Contains(c, `"@x/b": "~2.0.1"`) || !strings.Contains(c, `"@x/a": "workspace:*"`) {
		t.Fatalf("c/package.json:\n%s", c)
	}
	if !strings.Contains(files["packages/b/CHANGELOG.md"], "* @x/a bumped from ^1.0.0 to ^1.1.0") {
		t.Fatalf("b changelog:\n%s", files["packages/b/CHANGELOG.md"])
	}
	man := files[".release-please-manifest.json"]
	if man != "{\n  \"packages/a\": \"1.1.0\",\n  \"packages/b\": \"2.0.1\",\n  \"packages/c\": \"0.3.1\"\n}\n" {
		t.Fatalf("manifest:\n%s", man)
	}
}

func TestLinkedVersions(t *testing.T) {
	r := newTestRepo(t)
	r.commit("chore: init", map[string]string{"a/version.txt": "1.0.0\n", "b/version.txt": "1.0.0\n", "c/version.txt": "3.0.0\n"})
	for _, tag := range []string{"a-v1.0.0", "b-v1.0.0", "c-v3.0.0"} {
		r.git("tag", tag)
	}
	r.commit("feat: a feature", map[string]string{"a/x": "1"})
	r.commit("fix: c fix", map[string]string{"c/x": "1"})
	run := r.runner(`{"plugins":[{"type":"linked-versions","groupName":"ab","components":["a","b"]}],
		"packages":{"a":{"component":"a"},"b":{"component":"b"},"c":{"component":"c"}}}`,
		map[string]string{"a": "1.0.0", "b": "1.0.0", "c": "3.0.0"})
	cands, err := run.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := byPath(cands)
	if m["a"].Version.String() != "1.1.0" || m["b"] == nil || m["b"].Version.String() != "1.1.0" {
		t.Fatalf("linked versions wrong: %+v", m)
	}
	if m["c"].Version.String() != "3.0.1" {
		t.Fatalf("c: %s", m["c"].Version)
	}
}

func TestPathScopingAndExclude(t *testing.T) {
	r := newTestRepo(t)
	r.commit("chore: init", map[string]string{"version.txt": "1.0.0\n", "svc/version.txt": "1.0.0\n"})
	r.git("tag", "v1.0.0")
	r.git("tag", "svc-v1.0.0")
	r.commit("feat: docs site", map[string]string{"docs/index.md": "x"})
	r.commit("fix(svc): bug", map[string]string{"svc/main.go": "x"})
	run := r.runner(`{"packages":{".":{"exclude-paths":["docs","svc"]},"svc":{"component":"svc"}}}`,
		map[string]string{".": "1.0.0", "svc": "1.0.0"})
	cands, err := run.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Pkg.Path != "svc" || cands[0].Version.String() != "1.0.1" {
		t.Fatalf("unexpected candidates %+v", cands)
	}
}

func TestNoTagFallsBackToTagSearch(t *testing.T) {
	r := newTestRepo(t)
	r.commit("chore: init", map[string]string{"version.txt": "0.4.0\n"})
	r.git("tag", "v0.4.0")
	r.commit("feat!: breaking", nil)
	// No manifest entry at all: the latest tag is found and used.
	run := r.runner(`{"bump-minor-pre-major":true,"packages":{".":{}}}`, nil)
	cands, err := run.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Version.String() != "0.5.0" || cands[0].PrevTag != "v0.4.0" {
		t.Fatalf("unexpected %+v", cands[0])
	}
}

func TestTagNames(t *testing.T) {
	f := false
	v := semver.MustParse("1.2.3")
	cases := []struct {
		pkg  config.Package
		comp string
		want string
	}{
		{config.Package{}, "", "v1.2.3"},
		{config.Package{}, "api", "api-v1.2.3"},
		{config.Package{TagSeparator: "/"}, "api", "api/v1.2.3"},
		{config.Package{IncludeVInTag: &f}, "api", "api-1.2.3"},
		{config.Package{IncludeComponentInTag: &f}, "api", "v1.2.3"},
	}
	for _, c := range cases {
		got := TagName(&c.pkg, c.comp, v)
		if got != c.want {
			t.Errorf("got %s want %s", got, c.want)
		}
		back, ok := parseTag(&c.pkg, c.comp, got)
		if !ok || back.String() != "1.2.3" {
			t.Errorf("parseTag(%s) = %v %v", got, back, ok)
		}
	}
	if _, ok := parseTag(&config.Package{}, "", "api-v1.0.0"); ok {
		t.Error("root package must not match component tags")
	}
}

func TestReleaseDataRoundTrip(t *testing.T) {
	d := releaseData{V: 1, Releases: []releaseItem{{Path: "a--b", Version: "1.0.0", Tag: "x-->y"}}}
	body := "notes\n\n" + encodeData(d)
	got, ok := decodeData(body)
	if !ok || got.Releases[0].Tag != "x-->y" {
		t.Fatalf("roundtrip failed: %+v", got)
	}
}

func TestExtractNotes(t *testing.T) {
	cl := "# Changelog\n\n## [1.1.0](u) (2026-10-29)\n\n### Features\n\n* a\n\n## [1.0.1](u) (2026-01-01)\n\n* b\n\n## 1.0.0 (2025-01-01)\n\n* c\n"
	if got := ExtractNotes(cl, "1.1.0"); got != "## [1.1.0](u) (2026-10-29)\n\n### Features\n\n* a\n" {
		t.Fatalf("got %q", got)
	}
	if got := ExtractNotes(cl, "1.0.0"); got != "## 1.0.0 (2025-01-01)\n\n* c\n" {
		t.Fatalf("got %q", got)
	}
	if got := ExtractNotes(cl, "9.9.9"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestSeparatePullRequestsAndTitles(t *testing.T) {
	r := newTestRepo(t)
	r.commit("chore: init", map[string]string{"a/version.txt": "1.0.0\n", "b/version.txt": "1.0.0\n"})
	r.git("tag", "a-v1.0.0")
	r.git("tag", "b-v1.0.0")
	r.commit("feat: x", map[string]string{"a/x": "1", "b/x": "1"})
	run := r.runner(`{"separate-pull-requests":true,"packages":{"a":{},"b":{"pull-request-title-pattern":"release${component}: v${version}"}}}`,
		map[string]string{"a": "1.0.0", "b": "1.0.0"})
	cands, err := run.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gs := run.groups(cands)
	if len(gs) != 2 {
		t.Fatalf("want 2 groups, got %d", len(gs))
	}
	if gs[0].branch != "publish-please--branches--main--components--a" {
		t.Fatalf("branch %s", gs[0].branch)
	}
	if got := run.title(gs[0]); got != "chore(main): release a 1.1.0" {
		t.Fatalf("title %q", got)
	}
	if got := run.title(gs[1]); got != "release b: v1.1.0" {
		t.Fatalf("title %q", got)
	}
}
