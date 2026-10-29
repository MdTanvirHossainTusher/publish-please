//go:build e2e

// End-to-end test against a real Gitea/Forgejo server.
//
//	docker run -d -p 3300:3000 -e GITEA__security__INSTALL_LOCK=true gitea/gitea:1.24
//	PP_E2E_URL=http://localhost:3300 PP_E2E_USER=pp PP_E2E_TOKEN=... go test -tags e2e ./test/e2e/
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MdTanvirHossainTusher/publish-please/internal/cli"
)

type env struct {
	t     *testing.T
	url   string
	user  string
	token string
	repo  string
	dir   string
}

func setup(t *testing.T) *env {
	e := &env{t: t, url: os.Getenv("PP_E2E_URL"), user: os.Getenv("PP_E2E_USER"), token: os.Getenv("PP_E2E_TOKEN")}
	if e.url == "" || e.token == "" || e.user == "" {
		t.Skip("PP_E2E_URL, PP_E2E_USER and PP_E2E_TOKEN are required")
	}
	name := fmt.Sprintf("e2e-%d", time.Now().UnixNano())
	e.repo = e.user + "/" + name
	e.api("POST", "/user/repos", map[string]any{"name": name, "default_branch": "main"}, nil)
	e.dir = t.TempDir()
	e.git("init", "-q", "-b", "main")
	e.git("config", "user.name", "Dev")
	e.git("config", "user.email", "dev@example.com")
	e.git("remote", "add", "origin", fmt.Sprintf("%s/%s.git", strings.Replace(e.url, "://", "://"+e.user+":"+e.token+"@", 1), e.repo))
	return e
}

func (e *env) api(method, path string, in, out any) int {
	e.t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.url+"/api/v1"+path, body)
	req.Header.Set("Authorization", "token "+e.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 && resp.StatusCode != 404 {
		e.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, data)
	}
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			e.t.Fatalf("%s %s: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

func (e *env) git(args ...string) string {
	e.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = e.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (e *env) write(path, content string) {
	e.t.Helper()
	p := filepath.Join(e.dir, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) commit(msg string) {
	e.git("add", "-A")
	e.git("commit", "-q", "--allow-empty", "-m", msg)
}

// settle waits until Gitea has processed pushes (it marks repos non-empty
// and updates branches asynchronously).
func (e *env) settle() {
	e.t.Helper()
	head := e.git("rev-parse", "HEAD")
	for i := 0; i < 50; i++ {
		var b struct {
			Commit struct {
				ID string `json:"id"`
			} `json:"commit"`
		}
		var r struct {
			Empty bool `json:"empty"`
		}
		e.api("GET", "/repos/"+e.repo, nil, &r)
		if !r.Empty && e.api("GET", "/repos/"+e.repo+"/branches/main", nil, &b) == 200 && b.Commit.ID == head {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.t.Fatal("gitea did not process the push in time")
}

// merge merges a PR, retrying while Gitea is still checking mergeability.
func (e *env) merge(n int, style string) {
	e.t.Helper()
	for i := 0; i < 50; i++ {
		b, _ := json.Marshal(map[string]any{"Do": style})
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/repos/%s/pulls/%d/merge", e.url, e.repo, n), bytes.NewReader(b))
		req.Header.Set("Authorization", "token "+e.token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			e.t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode < 300 {
			e.git("pull", "-q", "--rebase", "origin", "main")
			e.settle()
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	e.t.Fatalf("could not merge PR #%d", n)
}

func (e *env) push(args ...string) {
	e.t.Helper()
	e.git(append([]string{"push", "-q", "origin", "main"}, args...)...)
	e.settle()
}

func (e *env) run(args ...string) string {
	e.t.Helper()
	var out, errb bytes.Buffer
	full := append(args, "--forge", "gitea", "--server-url", e.url, "--repo", e.repo, "--token", e.token, "--outputs-file", "")
	code := cli.Main(full, &out, &errb)
	e.t.Logf("$ publish-please %s\n%s%s", strings.Join(args, " "), errb.String(), out.String())
	if code != 0 {
		e.t.Fatalf("exit %d", code)
	}
	return out.String() + errb.String()
}

type pr struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	Head   struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (e *env) openPRs() []pr {
	var prs []pr
	e.api("GET", "/repos/"+e.repo+"/pulls?state=open", nil, &prs)
	return prs
}

func (e *env) file(ref, path string) string {
	e.t.Helper()
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/repos/%s/raw/%s?ref=%s", e.url, e.repo, path, ref), nil)
	req.Header.Set("Authorization", "token "+e.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		e.t.Fatalf("raw %s@%s: %d %s", path, ref, resp.StatusCode, b)
	}
	return string(b)
}

func mustContain(t *testing.T, what, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Fatalf("%s does not contain %q:\n%s", what, sub, s)
		}
	}
}

func TestGiteaReleaseCycle(t *testing.T) {
	e := setup(t)

	// A monorepo: a node package at the root and a simple package in packages/lib.
	e.write("release-please-config.json", `{
  "packages": {
    ".": {"release-type": "node", "exclude-paths": ["packages"]},
    "packages/lib": {"release-type": "simple", "component": "lib"}
  }
}
`)
	e.write(".release-please-manifest.json", `{".": "1.0.0", "packages/lib": "0.1.0"}`+"\n")
	e.write("package.json", "{\n  \"name\": \"demo\",\n  \"version\": \"1.0.0\",\n  \"private\": true\n}\n")
	e.write("package-lock.json", "{\n  \"name\": \"demo\",\n  \"version\": \"1.0.0\",\n  \"lockfileVersion\": 3,\n  \"packages\": {\n    \"\": {\n      \"name\": \"demo\",\n      \"version\": \"1.0.0\"\n    }\n  }\n}\n")
	e.write("packages/lib/version.txt", "0.1.0\n")
	e.write("src/version.js", "export const VERSION = '1.0.0'; // x-release-please-version\n")
	e.commit("chore: initial import")
	e.git("tag", "v1.0.0")
	e.git("tag", "lib-v0.1.0")
	e.write("src/index.js", "export const hi = () => 'hi'\n")
	e.commit("feat: add greeting")
	e.write("packages/lib/lib.sh", "echo lib\n")
	e.commit("fix(lib): handle empty input")
	e.write("README.md", "# demo\n")
	e.commit("docs: readme")
	e.push("--tags")

	// Add extra-files to prove generic markers work through config.
	e.write("release-please-config.json", `{
  "packages": {
    ".": {"release-type": "node", "exclude-paths": ["packages"], "extra-files": ["src/version.js"]},
    "packages/lib": {"release-type": "simple", "component": "lib"}
  }
}
`)
	e.commit("ci: track version.js")
	e.push()

	// 1. Release PR is opened with both packages.
	e.run("release-pr")
	prs := e.openPRs()
	if len(prs) != 1 {
		t.Fatalf("want 1 open PR, got %d", len(prs))
	}
	p := prs[0]
	if p.Title != "chore: release main" {
		t.Fatalf("title %q", p.Title)
	}
	if p.Head.Ref != "publish-please--branches--main" {
		t.Fatalf("branch %q", p.Head.Ref)
	}
	if len(p.Labels) == 0 || p.Labels[0].Name != "autorelease: pending" {
		t.Fatalf("labels %+v", p.Labels)
	}
	mustContain(t, "PR body", p.Body, "demo: 1.1.0", "lib: 0.1.1", "add greeting", "handle empty input", "publish-please:release-data")
	branch := p.Head.Ref
	mustContain(t, "package.json", e.file(branch, "package.json"), `"version": "1.1.0"`, `"private": true`)
	lock := e.file(branch, "package-lock.json")
	if strings.Count(lock, `"version": "1.1.0"`) != 2 {
		t.Fatalf("package-lock.json not fully updated:\n%s", lock)
	}
	mustContain(t, "version.js", e.file(branch, "src/version.js"), "VERSION = '1.1.0'")
	mustContain(t, "lib/version.txt", e.file(branch, "packages/lib/version.txt"), "0.1.1")
	mustContain(t, "manifest", e.file(branch, ".release-please-manifest.json"), `".": "1.1.0"`, `"packages/lib": "0.1.1"`)
	cl := e.file(branch, "CHANGELOG.md")
	mustContain(t, "CHANGELOG.md", cl, "## [1.1.0]("+e.url+"/"+e.repo+"/compare/v1.0.0...v1.1.0)", "### Features", "* add greeting")
	if strings.Contains(cl, "handle empty input") || strings.Contains(cl, "readme") {
		t.Fatalf("root changelog leaked lib/hidden commits:\n%s", cl)
	}
	mustContain(t, "lib CHANGELOG.md", e.file(branch, "packages/lib/CHANGELOG.md"), "### Bug Fixes", "handle empty input", "compare/lib-v0.1.0...lib-v0.1.1")

	// 2. Running again is a no-op for the branch.
	out := e.run("release-pr")
	mustContain(t, "second run", out, "already up to date")

	// 3. A new commit refreshes the same PR.
	e.git("pull", "-q", "--rebase", "origin", "main")
	e.write("src/extra.js", "export const x = 1\n")
	e.commit("feat(api): add extra endpoint")
	e.push()
	e.run("release-pr")
	prs = e.openPRs()
	if len(prs) != 1 || prs[0].Number != p.Number {
		t.Fatalf("expected the same PR to be updated, got %+v", prs)
	}
	mustContain(t, "updated body", prs[0].Body, "**api:** add extra endpoint")

	// 4. Merge, then release.
	e.merge(p.Number, "squash")
	out = e.run("release", "--json")
	mustContain(t, "release output", out, `"releases_created": "true"`, `"tag_name": "v1.1.0"`, `"packages/lib--tag_name": "lib-v0.1.1"`)

	var rel struct {
		TagName string `json:"tag_name"`
		Body    string `json:"body"`
		Name    string `json:"name"`
	}
	if e.api("GET", "/repos/"+e.repo+"/releases/tags/v1.1.0", nil, &rel) != 200 {
		t.Fatal("release v1.1.0 missing")
	}
	mustContain(t, "release notes", rel.Body, "add greeting", "add extra endpoint")
	if e.api("GET", "/repos/"+e.repo+"/releases/tags/lib-v0.1.1", nil, &rel) != 200 {
		t.Fatal("release lib-v0.1.1 missing")
	}
	if rel.Name != "lib: v0.1.1" {
		t.Fatalf("release name %q", rel.Name)
	}
	var merged pr
	e.api("GET", fmt.Sprintf("/repos/%s/pulls/%d", e.repo, p.Number), nil, &merged)
	if len(merged.Labels) != 1 || merged.Labels[0].Name != "autorelease: tagged" {
		t.Fatalf("labels after release: %+v", merged.Labels)
	}

	// 5. Releasing again does nothing; nothing new to release either.
	out = e.run("run")
	mustContain(t, "idempotent run", out, "no merged release PRs", "no releasable changes")
	if n := len(e.openPRs()); n != 0 {
		t.Fatalf("expected no open PRs, got %d", n)
	}

	// 6. A breaking change on the root package -> 2.0.0; `run` does both steps.
	e.git("pull", "-q", "--rebase", "origin", "main")
	e.write("src/index.js", "export const hello = () => 'hello'\n")
	e.commit("feat!: rename hi to hello\n\nBREAKING CHANGE: `hi` is now `hello`.")
	e.push()
	e.run("run")
	prs = e.openPRs()
	if len(prs) != 1 || prs[0].Title != "chore(main): release demo 2.0.0" {
		t.Fatalf("unexpected PRs %+v", prs)
	}
	mustContain(t, "major body", prs[0].Body, "⚠ BREAKING CHANGES", "`hi` is now `hello`.")
	if strings.Contains(prs[0].Body, "lib-v") {
		t.Fatalf("lib should not be released:\n%s", prs[0].Body)
	}

	// 7. Dry run changes nothing.
	out = e.run("release-pr", "--dry-run")
	mustContain(t, "dry run", out, "Would open release PR", "demo -> 2.0.0")
}

func TestGiteaCommitOverride(t *testing.T) {
	e := setup(t)
	e.write("version.txt", "1.0.0\n")
	e.commit("chore: init")
	e.git("tag", "v1.0.0")
	e.push("--tags")

	// A PR whose squash commit says "fix", but whose body overrides it.
	e.git("checkout", "-q", "-b", "topic")
	e.write("a.txt", "a\n")
	e.commit("fix: typo")
	e.git("push", "-q", "origin", "topic")
	var created pr
	e.api("POST", "/repos/"+e.repo+"/pulls", map[string]any{
		"head": "topic", "base": "main", "title": "fix: typo",
		"body": "Details.\n\nBEGIN_COMMIT_OVERRIDE\nfeat(core): real feature\nfix: real fix\nEND_COMMIT_OVERRIDE\n",
	}, &created)
	e.git("checkout", "-q", "main")
	e.merge(created.Number, "squash")

	e.run("release-pr", "--release-type", "simple")
	prs := e.openPRs()
	if len(prs) != 1 || prs[0].Title != "chore(main): release 1.1.0" {
		t.Fatalf("override not applied: %+v", prs)
	}
	mustContain(t, "body", prs[0].Body, "**core:** real feature", "real fix")
	if strings.Contains(prs[0].Body, "typo") {
		t.Fatalf("original commit leaked:\n%s", prs[0].Body)
	}
}

func TestGiteaSingleModeAndBootstrap(t *testing.T) {
	e := setup(t)
	e.write("version.txt", "0.0.0\n")
	e.commit("feat: first feature")
	e.push()

	// No config: --release-type works like release-please's action input.
	e.run("release-pr", "--release-type", "simple", "--path", ".")
	prs := e.openPRs()
	if len(prs) != 1 || prs[0].Title != "chore(main): release 1.0.0" {
		t.Fatalf("unexpected PRs %+v", prs)
	}
	mustContain(t, "version.txt", e.file(prs[0].Head.Ref, "version.txt"), "1.0.0")
	e.merge(prs[0].Number, "merge")
	out := e.run("release", "--release-type", "simple", "--json")
	mustContain(t, "release", out, `"tag_name": "v1.0.0"`)

	// Bootstrap picks up the released version from tags.
	e.run("bootstrap", "--release-type", "simple")
	prs = e.openPRs()
	if len(prs) != 1 || prs[0].Head.Ref != "publish-please--bootstrap" {
		t.Fatalf("unexpected PRs %+v", prs)
	}
	mustContain(t, "bootstrap manifest", e.file(prs[0].Head.Ref, ".release-please-manifest.json"), `".": "1.0.0"`)
}
