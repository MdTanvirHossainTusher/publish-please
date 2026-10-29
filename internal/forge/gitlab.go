package forge

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/MdTanvirHossainTusher/publish-please/internal/gitrepo"
)

// GitLab (gitlab.com and self-managed). Merge requests map to PullRequest
// using their project-scoped iid as Number.
type gitlab struct {
	c   *client
	o   Options
	pid string // url-encoded project path
}

func newGitLab(o Options) (Forge, error) {
	if o.WebURL == "" {
		o.WebURL = "https://gitlab.com"
	}
	if o.APIURL == "" {
		o.APIURL = o.WebURL + "/api/v4"
	}
	h := map[string]string{}
	if o.Token != "" {
		h["PRIVATE-TOKEN"] = o.Token
	}
	return &gitlab{c: &client{http: o.HTTPClient, base: o.APIURL, headers: h}, o: o, pid: url.PathEscape(o.Repo)}, nil
}

func (g *gitlab) Kind() string { return "gitlab" }

func (g *gitlab) proj() string { return "/projects/" + g.pid }

func (g *gitlab) GitAuth() *gitrepo.Auth {
	if g.o.Token == "" {
		return nil
	}
	return &gitrepo.Auth{Username: "oauth2", Password: g.o.Token}
}

func (g *gitlab) Repository(ctx context.Context) (*Repository, error) {
	var r struct {
		DefaultBranch string `json:"default_branch"`
		HTTPURL       string `json:"http_url_to_repo"`
		WebURL        string `json:"web_url"`
	}
	if _, err := g.c.do(ctx, "GET", g.proj(), nil, &r); err != nil {
		return nil, err
	}
	return &Repository{DefaultBranch: r.DefaultBranch, CloneURL: r.HTTPURL, WebURL: r.WebURL}, nil
}

type gitlabMR struct {
	IID             int      `json:"iid"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	SourceBranch    string   `json:"source_branch"`
	TargetBranch    string   `json:"target_branch"`
	Labels          []string `json:"labels"`
	State           string   `json:"state"`
	MergeCommitSHA  string   `json:"merge_commit_sha"`
	SquashCommitSHA string   `json:"squash_commit_sha"`
	WebURL          string   `json:"web_url"`
	Draft           bool     `json:"draft"`
}

func (m gitlabMR) toPR() *PullRequest {
	sha := m.MergeCommitSHA
	if sha == "" {
		sha = m.SquashCommitSHA
	}
	return &PullRequest{
		Number: m.IID, Title: m.Title, Body: m.Description,
		HeadBranch: m.SourceBranch, BaseBranch: m.TargetBranch, Labels: m.Labels,
		Merged: m.State == "merged", MergeCommitSHA: sha, URL: m.WebURL, Draft: m.Draft,
	}
}

func (g *gitlab) FindOpenPR(ctx context.Context, head, base string) (*PullRequest, error) {
	var mrs []gitlabMR
	q := fmt.Sprintf("%s/merge_requests?state=opened&source_branch=%s&target_branch=%s", g.proj(), url.QueryEscape(head), url.QueryEscape(base))
	if _, err := g.c.do(ctx, "GET", q, nil, &mrs); err != nil {
		return nil, err
	}
	if len(mrs) == 0 {
		return nil, nil
	}
	return mrs[0].toPR(), nil
}

func (g *gitlab) GetPR(ctx context.Context, n int) (*PullRequest, error) {
	var m gitlabMR
	if _, err := g.c.do(ctx, "GET", fmt.Sprintf("%s/merge_requests/%d", g.proj(), n), nil, &m); err != nil {
		return nil, err
	}
	return m.toPR(), nil
}

func (g *gitlab) CreatePR(ctx context.Context, in PRInput) (*PullRequest, error) {
	body := map[string]any{
		"source_branch": in.Head, "target_branch": in.Base,
		"title":       draftTitle(in.Title, in.Draft, "Draft:"),
		"description": in.Body, "labels": strings.Join(in.Labels, ","),
	}
	var m gitlabMR
	if _, err := g.c.do(ctx, "POST", g.proj()+"/merge_requests", body, &m); err != nil {
		return nil, err
	}
	return m.toPR(), nil
}

func (g *gitlab) UpdatePR(ctx context.Context, n int, in PRInput) (*PullRequest, error) {
	body := map[string]any{"title": draftTitle(in.Title, in.Draft, "Draft:"), "description": in.Body}
	var m gitlabMR
	if _, err := g.c.do(ctx, "PUT", fmt.Sprintf("%s/merge_requests/%d", g.proj(), n), body, &m); err != nil {
		return nil, err
	}
	return m.toPR(), nil
}

func (g *gitlab) ClosePR(ctx context.Context, n int) error {
	_, err := g.c.do(ctx, "PUT", fmt.Sprintf("%s/merge_requests/%d", g.proj(), n), map[string]any{"state_event": "close"}, nil)
	return err
}

func (g *gitlab) ListMergedPRs(ctx context.Context, base string, limit int) ([]PullRequest, error) {
	var out []PullRequest
	for page := 1; len(out) < limit && page <= 20; page++ {
		var mrs []gitlabMR
		q := fmt.Sprintf("%s/merge_requests?state=merged&target_branch=%s&order_by=updated_at&sort=desc&per_page=100&page=%d", g.proj(), url.QueryEscape(base), page)
		if _, err := g.c.do(ctx, "GET", q, nil, &mrs); err != nil {
			return nil, err
		}
		for _, m := range mrs {
			out = append(out, *m.toPR())
		}
		if len(mrs) < 100 {
			break
		}
	}
	return out, nil
}

func (g *gitlab) AddLabels(ctx context.Context, n int, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	_, err := g.c.do(ctx, "PUT", fmt.Sprintf("%s/merge_requests/%d", g.proj(), n), map[string]any{"add_labels": strings.Join(labels, ",")}, nil)
	return err
}

func (g *gitlab) RemoveLabels(ctx context.Context, n int, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	_, err := g.c.do(ctx, "PUT", fmt.Sprintf("%s/merge_requests/%d", g.proj(), n), map[string]any{"remove_labels": strings.Join(labels, ",")}, nil)
	return err
}

func (g *gitlab) Comment(ctx context.Context, n int, body string) error {
	_, err := g.c.do(ctx, "POST", fmt.Sprintf("%s/merge_requests/%d/notes", g.proj(), n), map[string]any{"body": body}, nil)
	return err
}

type gitlabRelease struct {
	TagName string `json:"tag_name"`
	Links   struct {
		Self string `json:"self"`
	} `json:"_links"`
}

func (g *gitlab) CreateRelease(ctx context.Context, in ReleaseInput) (*Release, error) {
	// GitLab has no draft or prerelease flags on releases.
	body := map[string]any{"tag_name": in.Tag, "ref": in.Target, "name": in.Name, "description": in.Notes}
	var r gitlabRelease
	if _, err := g.c.do(ctx, "POST", g.proj()+"/releases", body, &r); err != nil {
		return nil, err
	}
	return &Release{Tag: r.TagName, URL: g.releaseURL(r)}, nil
}

func (g *gitlab) releaseURL(r gitlabRelease) string {
	if r.Links.Self != "" {
		return r.Links.Self
	}
	return g.web() + "/-/releases/" + url.PathEscape(r.TagName)
}

func (g *gitlab) ReleaseByTag(ctx context.Context, tag string) (*Release, error) {
	var r gitlabRelease
	if _, err := g.c.do(ctx, "GET", g.proj()+"/releases/"+url.PathEscape(tag), nil, &r); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &Release{Tag: r.TagName, URL: g.releaseURL(r)}, nil
}

func (g *gitlab) web() string { return webBase(g.o) + "/" + g.o.Repo }

func (g *gitlab) CommitURL(sha string) string   { return g.web() + "/-/commit/" + sha }
func (g *gitlab) CompareURL(a, b string) string { return g.web() + "/-/compare/" + a + "..." + b }
func (g *gitlab) IssueURL(n int) string         { return fmt.Sprintf("%s/-/merge_requests/%d", g.web(), n) }
func (g *gitlab) IssuePrefix() string           { return "!" }
