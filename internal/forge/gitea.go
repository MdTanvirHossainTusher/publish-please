package forge

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/MdTanvirHossainTusher/publish-please/internal/gitrepo"
)

// Gitea and Forgejo (same API).
type gitea struct {
	c     *client
	o     Options
	owner string
	name  string

	mu     sync.Mutex
	labels map[string]int64 // name -> id cache
}

func newGitea(o Options) (Forge, error) {
	if o.APIURL == "" {
		if o.WebURL == "" {
			return nil, fmt.Errorf("gitea: --api-url or --server-url is required")
		}
		o.APIURL = o.WebURL + "/api/v1"
	}
	if o.WebURL == "" {
		o.WebURL = strings.TrimSuffix(o.APIURL, "/api/v1")
	}
	owner, name := splitRepo(o.Repo)
	h := map[string]string{}
	if o.Token != "" {
		h["Authorization"] = "token " + o.Token
	}
	return &gitea{c: &client{http: o.HTTPClient, base: o.APIURL, headers: h}, o: o, owner: owner, name: name}, nil
}

func (g *gitea) Kind() string { return "gitea" }

func (g *gitea) repoPath() string {
	return "/repos/" + pathEscape(g.owner) + "/" + pathEscape(g.name)
}

func (g *gitea) GitAuth() *gitrepo.Auth {
	if g.o.Token == "" {
		return nil
	}
	// Gitea accepts a token as the password for any username.
	return &gitrepo.Auth{Username: "oauth2", Password: g.o.Token}
}

func (g *gitea) Repository(ctx context.Context) (*Repository, error) {
	var r struct {
		DefaultBranch string `json:"default_branch"`
		CloneURL      string `json:"clone_url"`
		HTMLURL       string `json:"html_url"`
	}
	if _, err := g.c.do(ctx, "GET", g.repoPath(), nil, &r); err != nil {
		return nil, err
	}
	return &Repository{DefaultBranch: r.DefaultBranch, CloneURL: r.CloneURL, WebURL: r.HTMLURL}, nil
}

type giteaPR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Head   struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Labels []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"labels"`
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	HTMLURL        string `json:"html_url"`
	Draft          bool   `json:"draft"`
}

func (p giteaPR) toPR() *PullRequest {
	out := &PullRequest{
		Number: p.Number, Title: p.Title, Body: p.Body,
		HeadBranch: p.Head.Ref, BaseBranch: p.Base.Ref,
		Merged: p.Merged, MergeCommitSHA: p.MergeCommitSHA, URL: p.HTMLURL,
		Draft: p.Draft || strings.HasPrefix(p.Title, "WIP:") || strings.HasPrefix(p.Title, "[WIP]"),
	}
	for _, l := range p.Labels {
		out.Labels = append(out.Labels, l.Name)
	}
	return out
}

func (g *gitea) listPRs(ctx context.Context, query string, maxPages int, keep func(*PullRequest) bool) ([]PullRequest, error) {
	var out []PullRequest
	for page := 1; page <= maxPages; page++ {
		var prs []giteaPR
		if _, err := g.c.do(ctx, "GET", fmt.Sprintf("%s/pulls?%s&limit=50&page=%d", g.repoPath(), query, page), nil, &prs); err != nil {
			// Gitea answers 404 while a freshly pushed repo is still flagged empty.
			if IsNotFound(err) {
				return out, nil
			}
			return nil, err
		}
		for _, p := range prs {
			pr := p.toPR()
			if keep(pr) {
				out = append(out, *pr)
			}
		}
		if len(prs) < 50 {
			break
		}
	}
	return out, nil
}

func (g *gitea) FindOpenPR(ctx context.Context, head, base string) (*PullRequest, error) {
	prs, err := g.listPRs(ctx, "state=open", 20, func(p *PullRequest) bool {
		return p.HeadBranch == head && p.BaseBranch == base
	})
	if err != nil || len(prs) == 0 {
		return nil, err
	}
	return &prs[0], nil
}

func (g *gitea) GetPR(ctx context.Context, n int) (*PullRequest, error) {
	var p giteaPR
	if _, err := g.c.do(ctx, "GET", fmt.Sprintf("%s/pulls/%d", g.repoPath(), n), nil, &p); err != nil {
		return nil, err
	}
	return p.toPR(), nil
}

func draftTitle(title string, draft bool, prefix string) string {
	if draft && !strings.HasPrefix(title, prefix) {
		return prefix + " " + title
	}
	return title
}

func (g *gitea) CreatePR(ctx context.Context, in PRInput) (*PullRequest, error) {
	ids, err := g.labelIDs(ctx, in.Labels)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"head": in.Head, "base": in.Base, "body": in.Body, "labels": ids,
		"title": draftTitle(in.Title, in.Draft, "WIP:"),
	}
	var p giteaPR
	if _, err := g.c.do(ctx, "POST", g.repoPath()+"/pulls", body, &p); err != nil {
		return nil, err
	}
	return p.toPR(), nil
}

func (g *gitea) UpdatePR(ctx context.Context, n int, in PRInput) (*PullRequest, error) {
	body := map[string]any{"title": draftTitle(in.Title, in.Draft, "WIP:"), "body": in.Body}
	var p giteaPR
	if _, err := g.c.do(ctx, "PATCH", fmt.Sprintf("%s/pulls/%d", g.repoPath(), n), body, &p); err != nil {
		return nil, err
	}
	return p.toPR(), nil
}

func (g *gitea) ClosePR(ctx context.Context, n int) error {
	_, err := g.c.do(ctx, "PATCH", fmt.Sprintf("%s/pulls/%d", g.repoPath(), n), map[string]any{"state": "closed"}, nil)
	return err
}

func (g *gitea) ListMergedPRs(ctx context.Context, base string, limit int) ([]PullRequest, error) {
	pages := (limit + 49) / 50
	if pages < 1 {
		pages = 1
	}
	return g.listPRs(ctx, "state=closed&sort=recentupdate", pages, func(p *PullRequest) bool {
		return p.Merged && p.BaseBranch == base
	})
}

// labelIDs resolves label names to IDs, creating missing repo labels.
func (g *gitea) labelIDs(ctx context.Context, names []string) ([]int64, error) {
	if len(names) == 0 {
		return []int64{}, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.labels == nil {
		g.labels = map[string]int64{}
		for page := 1; page <= 20; page++ {
			var ls []struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			}
			if _, err := g.c.do(ctx, "GET", fmt.Sprintf("%s/labels?limit=50&page=%d", g.repoPath(), page), nil, &ls); err != nil {
				return nil, err
			}
			for _, l := range ls {
				g.labels[l.Name] = l.ID
			}
			if len(ls) < 50 {
				break
			}
		}
	}
	ids := make([]int64, 0, len(names))
	for _, n := range names {
		id, ok := g.labels[n]
		if !ok {
			var l struct {
				ID int64 `json:"id"`
			}
			if _, err := g.c.do(ctx, "POST", g.repoPath()+"/labels", map[string]any{"name": n, "color": "#ededed"}, &l); err != nil {
				return nil, fmt.Errorf("create label %q: %w", n, err)
			}
			id = l.ID
			g.labels[n] = id
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (g *gitea) AddLabels(ctx context.Context, n int, labels []string) error {
	ids, err := g.labelIDs(ctx, labels)
	if err != nil || len(ids) == 0 {
		return err
	}
	_, err = g.c.do(ctx, "POST", fmt.Sprintf("%s/issues/%d/labels", g.repoPath(), n), map[string]any{"labels": ids}, nil)
	return err
}

func (g *gitea) RemoveLabels(ctx context.Context, n int, labels []string) error {
	ids, err := g.labelIDs(ctx, labels)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := g.c.do(ctx, "DELETE", fmt.Sprintf("%s/issues/%d/labels/%d", g.repoPath(), n, id), nil, nil); err != nil && !IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (g *gitea) Comment(ctx context.Context, n int, body string) error {
	_, err := g.c.do(ctx, "POST", fmt.Sprintf("%s/issues/%d/comments", g.repoPath(), n), map[string]any{"body": body}, nil)
	return err
}

type giteaRelease struct {
	ID      int64  `json:"id"`
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Draft   bool   `json:"draft"`
}

func (g *gitea) toRelease(r giteaRelease) *Release {
	return &Release{
		ID: r.ID, Tag: r.TagName, URL: r.HTMLURL, Draft: r.Draft,
		UploadURL: fmt.Sprintf("%s%s/releases/%d/assets", g.o.APIURL, g.repoPath(), r.ID),
	}
}

func (g *gitea) CreateRelease(ctx context.Context, in ReleaseInput) (*Release, error) {
	body := map[string]any{
		"tag_name": in.Tag, "target_commitish": in.Target, "name": in.Name,
		"body": in.Notes, "draft": in.Draft, "prerelease": in.Prerelease,
	}
	var r giteaRelease
	if _, err := g.c.do(ctx, "POST", g.repoPath()+"/releases", body, &r); err != nil {
		return nil, err
	}
	return g.toRelease(r), nil
}

func (g *gitea) ReleaseByTag(ctx context.Context, tag string) (*Release, error) {
	var r giteaRelease
	if _, err := g.c.do(ctx, "GET", g.repoPath()+"/releases/tags/"+pathEscape(tag), nil, &r); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return g.toRelease(r), nil
}

func (g *gitea) web() string { return webBase(g.o) + "/" + g.owner + "/" + g.name }

func (g *gitea) CommitURL(sha string) string { return g.web() + "/commit/" + sha }
func (g *gitea) CompareURL(a, b string) string {
	return g.web() + "/compare/" + a + "..." + b
}
func (g *gitea) IssueURL(n int) string { return fmt.Sprintf("%s/issues/%d", g.web(), n) }
func (g *gitea) IssuePrefix() string   { return "#" }
