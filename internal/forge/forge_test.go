package forge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type call struct {
	Method, Path, Query, Body string
	Header                    http.Header
}

// fake records requests and answers from a route table keyed by "METHOD /path".
type fake struct {
	mu     sync.Mutex
	calls  []call
	routes map[string]string
	srv    *httptest.Server
}

func newFake(t *testing.T, routes map[string]string) *fake {
	f := &fake{routes: routes}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, call{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, string(b), r.Header.Clone()})
		f.mu.Unlock()
		key := r.Method + " " + r.URL.EscapedPath()
		resp, ok := f.routes[key]
		if !ok {
			w.WriteHeader(404)
			io.WriteString(w, `{"message":"not found"}`)
			return
		}
		io.WriteString(w, resp)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) find(t *testing.T, method, path string) call {
	t.Helper()
	for _, c := range f.calls {
		if c.Method == method && c.Path == path {
			return c
		}
	}
	t.Fatalf("no %s %s call; calls: %+v", method, path, f.calls)
	return call{}
}

func jsonBody(t *testing.T, c call) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(c.Body), &m); err != nil {
		t.Fatalf("body %q: %v", c.Body, err)
	}
	return m
}

func TestGitHubCreatePRAndRelease(t *testing.T) {
	f := newFake(t, map[string]string{
		"POST /repos/o/r/pulls":           `{"number":5,"title":"t","head":{"ref":"b"},"base":{"ref":"main"},"html_url":"u"}`,
		"POST /repos/o/r/issues/5/labels": `[]`,
		"POST /repos/o/r/releases":        `{"id":9,"tag_name":"v1.0.0","html_url":"rel","upload_url":"https://uploads/x/assets{?name,label}"}`,
		"GET /repos/o/r/pulls":            `[{"number":3,"merged_at":"2026-01-01","merge_commit_sha":"abc","head":{"ref":"publish-please--branches--main"},"base":{"ref":"main"},"labels":[{"name":"autorelease: pending"}]},{"number":4,"merged_at":null}]`,
	})
	g, err := New(Options{Kind: "github", APIURL: f.srv.URL, WebURL: "https://github.com", Repo: "o/r", Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pr, err := g.CreatePR(ctx, PRInput{Title: "t", Head: "b", Base: "main", Labels: []string{"autorelease: pending"}, Draft: true})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 5 || !pr.HasLabel("autorelease: pending") {
		t.Fatalf("pr %+v", pr)
	}
	c := f.find(t, "POST", "/repos/o/r/pulls")
	if c.Header.Get("Authorization") != "Bearer tok" || jsonBody(t, c)["draft"] != true {
		t.Fatalf("bad create call %+v", c)
	}
	if lbl := jsonBody(t, f.find(t, "POST", "/repos/o/r/issues/5/labels")); lbl["labels"].([]any)[0] != "autorelease: pending" {
		t.Fatalf("labels %v", lbl)
	}
	rel, err := g.CreateRelease(ctx, ReleaseInput{Tag: "v1.0.0", Target: "abc", Name: "v1.0.0", Notes: "n", Prerelease: true})
	if err != nil {
		t.Fatal(err)
	}
	if rel.UploadURL != "https://uploads/x/assets" {
		t.Fatalf("upload url %q", rel.UploadURL)
	}
	if b := jsonBody(t, f.find(t, "POST", "/repos/o/r/releases")); b["target_commitish"] != "abc" || b["prerelease"] != true {
		t.Fatalf("release body %v", b)
	}
	merged, err := g.ListMergedPRs(ctx, "main", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 1 || merged[0].MergeCommitSHA != "abc" {
		t.Fatalf("merged %+v", merged)
	}
	if g.CompareURL("v1", "v2") != "https://github.com/o/r/compare/v1...v2" {
		t.Fatal(g.CompareURL("v1", "v2"))
	}
	if a := g.GitAuth(); a.Username != "x-access-token" || a.Password != "tok" {
		t.Fatalf("git auth %+v", a)
	}
}

func TestGitLabMergeRequests(t *testing.T) {
	proj := "/projects/grp%2Fsub%2Frepo"
	f := newFake(t, map[string]string{
		"POST " + proj + "/merge_requests":         `{"iid":7,"title":"Draft: t","source_branch":"b","target_branch":"main","labels":["autorelease: pending"],"state":"opened","web_url":"mr"}`,
		"GET " + proj + "/merge_requests":          `[{"iid":2,"state":"merged","squash_commit_sha":"sq","source_branch":"b","target_branch":"main","labels":["autorelease: pending"]}]`,
		"PUT " + proj + "/merge_requests/2":        `{"iid":2}`,
		"POST " + proj + "/releases":               `{"tag_name":"v1.0.0","_links":{"self":"https://gitlab.com/grp/sub/repo/-/releases/v1.0.0"}}`,
		"POST " + proj + "/merge_requests/2/notes": `{}`,
	})
	g, err := New(Options{Kind: "gitlab", APIURL: f.srv.URL, WebURL: "https://gitlab.com", Repo: "grp/sub/repo", Token: "glpat"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pr, err := g.CreatePR(ctx, PRInput{Title: "t", Head: "b", Base: "main", Labels: []string{"autorelease: pending", "x"}, Draft: true})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 7 {
		t.Fatalf("pr %+v", pr)
	}
	c := f.find(t, "POST", proj+"/merge_requests")
	b := jsonBody(t, c)
	if c.Header.Get("PRIVATE-TOKEN") != "glpat" || b["title"] != "Draft: t" || b["labels"] != "autorelease: pending,x" || b["source_branch"] != "b" {
		t.Fatalf("create call %+v %v", c, b)
	}
	merged, err := g.ListMergedPRs(ctx, "main", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 1 || merged[0].MergeCommitSHA != "sq" || !merged[0].Merged {
		t.Fatalf("merged %+v", merged)
	}
	if err := g.RemoveLabels(ctx, 2, []string{"autorelease: pending"}); err != nil {
		t.Fatal(err)
	}
	if b := jsonBody(t, f.find(t, "PUT", proj+"/merge_requests/2")); b["remove_labels"] != "autorelease: pending" {
		t.Fatalf("remove labels %v", b)
	}
	rel, err := g.CreateRelease(ctx, ReleaseInput{Tag: "v1.0.0", Target: "sq", Notes: "n"})
	if err != nil || !strings.HasSuffix(rel.URL, "/-/releases/v1.0.0") {
		t.Fatalf("release %+v %v", rel, err)
	}
	if b := jsonBody(t, f.find(t, "POST", proj+"/releases")); b["ref"] != "sq" || b["description"] != "n" {
		t.Fatalf("release body %v", b)
	}
	if g.IssueURL(3) != "https://gitlab.com/grp/sub/repo/-/merge_requests/3" || g.IssuePrefix() != "!" {
		t.Fatal(g.IssueURL(3))
	}
}

func TestGiteaLabelsResolvedToIDs(t *testing.T) {
	f := newFake(t, map[string]string{
		"GET /api/v1/repos/o/r/labels":                `[{"id":11,"name":"autorelease: pending"}]`,
		"POST /api/v1/repos/o/r/labels":               `{"id":12,"name":"autorelease: tagged"}`,
		"POST /api/v1/repos/o/r/issues/4/labels":      `[]`,
		"DELETE /api/v1/repos/o/r/issues/4/labels/11": ``,
	})
	g, err := New(Options{Kind: "forgejo", WebURL: f.srv.URL, Repo: "o/r", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := g.AddLabels(ctx, 4, []string{"autorelease: pending", "autorelease: tagged"}); err != nil {
		t.Fatal(err)
	}
	b := jsonBody(t, f.find(t, "POST", "/api/v1/repos/o/r/issues/4/labels"))
	ids := b["labels"].([]any)
	if len(ids) != 2 || ids[0] != float64(11) || ids[1] != float64(12) {
		t.Fatalf("ids %v", ids)
	}
	if err := g.RemoveLabels(ctx, 4, []string{"autorelease: pending"}); err != nil {
		t.Fatal(err)
	}
	f.find(t, "DELETE", "/api/v1/repos/o/r/issues/4/labels/11")
	if c := f.find(t, "GET", "/api/v1/repos/o/r/labels"); c.Header.Get("Authorization") != "token t" {
		t.Fatalf("auth %q", c.Header.Get("Authorization"))
	}
	// labels are cached: only one listing
	n := 0
	for _, c := range f.calls {
		if c.Method == "GET" && c.Path == "/api/v1/repos/o/r/labels" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("labels listed %d times", n)
	}
}

func TestRetryOn5xx(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(502)
			return
		}
		io.WriteString(w, `{"default_branch":"main"}`)
	}))
	defer srv.Close()
	g, _ := New(Options{Kind: "gitea", APIURL: srv.URL, Repo: "o/r"})
	info, err := g.Repository(context.Background())
	if err != nil || info.DefaultBranch != "main" || n != 2 {
		t.Fatalf("info %+v err %v calls %d", info, err, n)
	}
}

func TestBitbucketBodyRoundTrip(t *testing.T) {
	body := "## Release\n\n<!-- publish-please:release-data eyJhIjoxfQ== -->\n<!-- keep (parens) -->"
	enc := bbEncodeBody(body, []string{"autorelease: pending"})
	if strings.Contains(enc, "<!-- publish-please:release-data") || !strings.Contains(enc, "[//]: # (publish-please:release-data eyJhIjoxfQ==)") {
		t.Fatalf("encoded %q", enc)
	}
	if !strings.Contains(enc, "<!-- keep (parens) -->") {
		t.Fatalf("comment with parens must stay untouched: %q", enc)
	}
	got, labels := bbDecodeBody(strings.ReplaceAll(enc, "\n", "\r\n"))
	if got != body || len(labels) != 1 || labels[0] != "autorelease: pending" {
		t.Fatalf("decoded %q %v", got, labels)
	}
	if _, l := bbDecodeBody("plain"); l != nil {
		t.Fatalf("labels %v", l)
	}
}

func TestBitbucketPullRequestsAndTags(t *testing.T) {
	repo := "/repositories/ws/app"
	pending := bbEncodeBody("notes", []string{"autorelease: pending"})
	desc, _ := json.Marshal(pending)
	f := newFake(t, map[string]string{
		"GET " + repo:                               `{"mainbranch":{"name":"main"},"links":{"html":{"href":"https://bitbucket.org/ws/app"},"clone":[{"name":"ssh","href":"git@bitbucket.org:ws/app.git"},{"name":"https","href":"https://me@bitbucket.org/ws/app.git"}]}}`,
		"POST " + repo + "/pullrequests":            `{"id":5,"title":"t","state":"OPEN","draft":true,"description":` + string(desc) + `,"source":{"branch":{"name":"b"}},"destination":{"branch":{"name":"main"}},"links":{"html":{"href":"https://bitbucket.org/ws/app/pull-requests/5"}}}`,
		"GET " + repo + "/pullrequests":             `{"values":[{"id":3,"state":"MERGED","description":` + string(desc) + `,"merge_commit":{"hash":"abc123def456"},"source":{"branch":{"name":"publish-please--branches--main"}},"destination":{"branch":{"name":"main"}}},{"id":2,"state":"MERGED","description":"x"}]}`,
		"GET " + repo + "/pullrequests/3/comments":  `{"values":[{"content":{"raw":"publish-please: removed label ` + "`autorelease: pending`" + `\npublish-please: added label ` + "`autorelease: tagged`" + `"}}]}`,
		"GET " + repo + "/pullrequests/4":           `{"id":4,"state":"MERGED","description":` + string(desc) + `}`,
		"GET " + repo + "/pullrequests/4/comments":  `{"values":[]}`,
		"POST " + repo + "/pullrequests/4/comments": `{}`,
		"POST " + repo + "/refs/tags":               `{"name":"v1.0.0","target":{"hash":"abc"}}`,
	})
	g, err := New(Options{Kind: "bitbucket", APIURL: f.srv.URL, Repo: "ws/app", Token: "atok"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	info, err := g.Repository(ctx)
	if err != nil || info.DefaultBranch != "main" || info.CloneURL != "https://bitbucket.org/ws/app.git" {
		t.Fatalf("repo %+v %v", info, err)
	}
	pr, err := g.CreatePR(ctx, PRInput{Title: "t", Body: "notes", Head: "b", Base: "main", Labels: []string{"autorelease: pending"}, Draft: true})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 5 || pr.Body != "notes" || !pr.HasLabel("autorelease: pending") || !pr.Draft {
		t.Fatalf("pr %+v", pr)
	}
	c := f.find(t, "POST", repo+"/pullrequests")
	b := jsonBody(t, c)
	if c.Header.Get("Authorization") != "Bearer atok" || b["draft"] != true || b["description"] != pending {
		t.Fatalf("create call %+v %v", c, b)
	}
	merged, err := g.ListMergedPRs(ctx, "main", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 2 || merged[0].MergeCommitSHA != "abc123def456" || !merged[0].Merged {
		t.Fatalf("merged %+v", merged)
	}
	if merged[0].HasLabel("autorelease: pending") || !merged[0].HasLabel("autorelease: tagged") {
		t.Fatalf("label replay %v", merged[0].Labels)
	}
	if merged[1].Labels != nil {
		t.Fatalf("foreign PR labels %v", merged[1].Labels)
	}
	if err := g.RemoveLabels(ctx, 4, []string{"autorelease: pending"}); err != nil {
		t.Fatal(err)
	}
	if b := jsonBody(t, f.find(t, "POST", repo+"/pullrequests/4/comments")); !strings.Contains(b["content"].(map[string]any)["raw"].(string), "removed label `autorelease: pending`") {
		t.Fatalf("comment %v", b)
	}
	rel, err := g.CreateRelease(ctx, ReleaseInput{Tag: "v1.0.0", Target: "abc", Name: "v1.0.0", Notes: "n"})
	if err != nil || rel.URL != "https://bitbucket.org/ws/app/src/v1.0.0/" {
		t.Fatalf("release %+v %v", rel, err)
	}
	if b := jsonBody(t, f.find(t, "POST", repo+"/refs/tags")); b["target"].(map[string]any)["hash"] != "abc" || b["message"] != "v1.0.0\n\nn" {
		t.Fatalf("tag body %v", b)
	}
	if r, err := g.ReleaseByTag(ctx, "v9"); r != nil || err != nil {
		t.Fatalf("missing tag %+v %v", r, err)
	}
	if g.CompareURL("v1", "v2") != "https://bitbucket.org/ws/app/branches/compare/v2%0Dv1#diff" || g.IssueURL(3) != "https://bitbucket.org/ws/app/pull-requests/3" {
		t.Fatal(g.CompareURL("v1", "v2"), g.IssueURL(3))
	}
	if a := g.GitAuth(); a.Username != "x-token-auth" || a.Password != "atok" {
		t.Fatalf("git auth %+v", a)
	}
	g2, _ := New(Options{Kind: "bitbucket", APIURL: f.srv.URL, Repo: "ws/app", Token: "me@example.com:apitok"})
	if a := g2.GitAuth(); a.Username != "x-bitbucket-api-token-auth" || a.Password != "apitok" {
		t.Fatalf("api token git auth %+v", a)
	}
	g3, _ := New(Options{Kind: "bitbucket", APIURL: f.srv.URL, Repo: "ws/app", Token: "me:apppw"})
	if a := g3.GitAuth(); a.Username != "me" || a.Password != "apppw" {
		t.Fatalf("app password git auth %+v", a)
	}
}
