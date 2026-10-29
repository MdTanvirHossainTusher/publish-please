package forge

import (
	"context"
	"fmt"
	"strings"

	"github.com/MdTanvirHossainTusher/publish-please/internal/gitrepo"
)

// GitHub and GitHub Enterprise Server.
type github struct {
	c     *client
	o     Options
	owner string
	name  string
}

func newGitHub(o Options) (Forge, error) {
	if o.WebURL == "" {
		o.WebURL = "https://github.com"
	}
	if o.APIURL == "" {
		if o.WebURL == "https://github.com" {
			o.APIURL = "https://api.github.com"
		} else {
			o.APIURL = o.WebURL + "/api/v3"
		}
	}
	owner, name := splitRepo(o.Repo)
	h := map[string]string{"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}
	if o.Token != "" {
		h["Authorization"] = "Bearer " + o.Token
	}
	return &github{c: &client{http: o.HTTPClient, base: o.APIURL, headers: h}, o: o, owner: owner, name: name}, nil
}

func (g *github) Kind() string { return "github" }

func (g *github) repoPath() string {
	return "/repos/" + pathEscape(g.owner) + "/" + pathEscape(g.name)
}

func (g *github) GitAuth() *gitrepo.Auth {
	if g.o.Token == "" {
		return nil
	}
	return &gitrepo.Auth{Username: "x-access-token", Password: g.o.Token}
}

func (g *github) Repository(ctx context.Context) (*Repository, error) {
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

type githubPR struct {
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
		Name string `json:"name"`
	} `json:"labels"`
	MergedAt       *string `json:"merged_at"`
	MergeCommitSHA string  `json:"merge_commit_sha"`
	HTMLURL        string  `json:"html_url"`
	Draft          bool    `json:"draft"`
}

func (p githubPR) toPR() *PullRequest {
	out := &PullRequest{
		Number: p.Number, Title: p.Title, Body: p.Body,
		HeadBranch: p.Head.Ref, BaseBranch: p.Base.Ref,
		Merged: p.MergedAt != nil, MergeCommitSHA: p.MergeCommitSHA, URL: p.HTMLURL, Draft: p.Draft,
	}
	for _, l := range p.Labels {
		out.Labels = append(out.Labels, l.Name)
	}
	return out
}

func (g *github) FindOpenPR(ctx context.Context, head, base string) (*PullRequest, error) {
	var prs []githubPR
	q := fmt.Sprintf("%s/pulls?state=open&head=%s:%s&base=%s&per_page=10", g.repoPath(), g.owner, pathEscape(head), pathEscape(base))
	if _, err := g.c.do(ctx, "GET", q, nil, &prs); err != nil {
		return nil, err
	}
	for _, p := range prs {
		if p.Head.Ref == head {
			return p.toPR(), nil
		}
	}
	return nil, nil
}

func (g *github) GetPR(ctx context.Context, n int) (*PullRequest, error) {
	var p githubPR
	if _, err := g.c.do(ctx, "GET", fmt.Sprintf("%s/pulls/%d", g.repoPath(), n), nil, &p); err != nil {
		return nil, err
	}
	return p.toPR(), nil
}

func (g *github) CreatePR(ctx context.Context, in PRInput) (*PullRequest, error) {
	body := map[string]any{"title": in.Title, "head": in.Head, "base": in.Base, "body": in.Body, "draft": in.Draft}
	var p githubPR
	if _, err := g.c.do(ctx, "POST", g.repoPath()+"/pulls", body, &p); err != nil {
		return nil, err
	}
	if err := g.AddLabels(ctx, p.Number, in.Labels); err != nil {
		return nil, err
	}
	pr := p.toPR()
	pr.Labels = append(pr.Labels, in.Labels...)
	return pr, nil
}

func (g *github) UpdatePR(ctx context.Context, n int, in PRInput) (*PullRequest, error) {
	var p githubPR
	if _, err := g.c.do(ctx, "PATCH", fmt.Sprintf("%s/pulls/%d", g.repoPath(), n), map[string]any{"title": in.Title, "body": in.Body}, &p); err != nil {
		return nil, err
	}
	return p.toPR(), nil
}

func (g *github) ClosePR(ctx context.Context, n int) error {
	_, err := g.c.do(ctx, "PATCH", fmt.Sprintf("%s/pulls/%d", g.repoPath(), n), map[string]any{"state": "closed"}, nil)
	return err
}

func (g *github) ListMergedPRs(ctx context.Context, base string, limit int) ([]PullRequest, error) {
	var out []PullRequest
	for page := 1; len(out) < limit && page <= 20; page++ {
		var prs []githubPR
		q := fmt.Sprintf("%s/pulls?state=closed&base=%s&sort=updated&direction=desc&per_page=100&page=%d", g.repoPath(), pathEscape(base), page)
		if _, err := g.c.do(ctx, "GET", q, nil, &prs); err != nil {
			return nil, err
		}
		for _, p := range prs {
			if p.MergedAt != nil {
				out = append(out, *p.toPR())
			}
		}
		if len(prs) < 100 {
			break
		}
	}
	return out, nil
}

func (g *github) AddLabels(ctx context.Context, n int, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	_, err := g.c.do(ctx, "POST", fmt.Sprintf("%s/issues/%d/labels", g.repoPath(), n), map[string]any{"labels": labels}, nil)
	return err
}

func (g *github) RemoveLabels(ctx context.Context, n int, labels []string) error {
	for _, l := range labels {
		if _, err := g.c.do(ctx, "DELETE", fmt.Sprintf("%s/issues/%d/labels/%s", g.repoPath(), n, pathEscape(l)), nil, nil); err != nil && !IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (g *github) Comment(ctx context.Context, n int, body string) error {
	_, err := g.c.do(ctx, "POST", fmt.Sprintf("%s/issues/%d/comments", g.repoPath(), n), map[string]any{"body": body}, nil)
	return err
}

type githubRelease struct {
	ID        int64  `json:"id"`
	TagName   string `json:"tag_name"`
	HTMLURL   string `json:"html_url"`
	UploadURL string `json:"upload_url"`
	Draft     bool   `json:"draft"`
}

func (r githubRelease) toRelease() *Release {
	up := r.UploadURL
	if i := strings.Index(up, "{"); i >= 0 {
		up = up[:i]
	}
	return &Release{ID: r.ID, Tag: r.TagName, URL: r.HTMLURL, UploadURL: up, Draft: r.Draft}
}

func (g *github) CreateRelease(ctx context.Context, in ReleaseInput) (*Release, error) {
	body := map[string]any{
		"tag_name": in.Tag, "target_commitish": in.Target, "name": in.Name,
		"body": in.Notes, "draft": in.Draft, "prerelease": in.Prerelease,
	}
	var r githubRelease
	if _, err := g.c.do(ctx, "POST", g.repoPath()+"/releases", body, &r); err != nil {
		return nil, err
	}
	return r.toRelease(), nil
}

func (g *github) ReleaseByTag(ctx context.Context, tag string) (*Release, error) {
	var r githubRelease
	if _, err := g.c.do(ctx, "GET", g.repoPath()+"/releases/tags/"+pathEscape(tag), nil, &r); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return r.toRelease(), nil
}

func (g *github) web() string { return webBase(g.o) + "/" + g.owner + "/" + g.name }

func (g *github) CommitURL(sha string) string   { return g.web() + "/commit/" + sha }
func (g *github) CompareURL(a, b string) string { return g.web() + "/compare/" + a + "..." + b }
func (g *github) IssueURL(n int) string         { return fmt.Sprintf("%s/issues/%d", g.web(), n) }
func (g *github) IssuePrefix() string           { return "#" }
