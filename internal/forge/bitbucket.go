package forge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/MdTanvirHossainTusher/publish-please/internal/gitrepo"
)

// Bitbucket Cloud (bitbucket.org, REST API 2.0).
//
// Bitbucket has neither pull request labels nor releases, so:
//   - Labels are emulated. The labels a PR is opened with are kept in a hidden
//     line of its description; later changes to a merged or declined PR (which
//     can no longer be edited) are recorded as "publish-please: added/removed
//     label" comments and replayed when the PR is read.
//   - A release is the tag itself. The release notes go in the tag message.
//
// Bitbucket markdown escapes HTML, so the hidden <!-- ... --> markers
// publish-please writes into PR bodies are stored as invisible link reference
// definitions ([//]: # (...)) and turned back into comments on read.
type bitbucket struct {
	c         *client
	o         Options
	workspace string
	slug      string
	gitUser   string
	gitPass   string
}

func newBitbucket(o Options) (Forge, error) {
	if o.WebURL == "" {
		o.WebURL = "https://bitbucket.org"
	}
	if o.APIURL == "" {
		if o.WebURL != "https://bitbucket.org" {
			return nil, fmt.Errorf("only Bitbucket Cloud is supported; pass --api-url for a non-standard Bitbucket Cloud host")
		}
		o.APIURL = "https://api.bitbucket.org/2.0"
	}
	ws, slug := splitRepo(o.Repo)
	b := &bitbucket{o: o, workspace: ws, slug: slug}
	h := map[string]string{}
	if o.Token != "" {
		if user, secret, ok := strings.Cut(o.Token, ":"); ok {
			// username:app-password, or email:API-token.
			h["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(o.Token))
			b.gitUser, b.gitPass = user, secret
			if strings.Contains(user, "@") {
				b.gitUser = "x-bitbucket-api-token-auth"
			}
		} else {
			// Repository, project or workspace access token.
			h["Authorization"] = "Bearer " + o.Token
			b.gitUser, b.gitPass = "x-token-auth", o.Token
		}
	}
	b.c = &client{http: o.HTTPClient, base: o.APIURL, headers: h}
	return b, nil
}

func (b *bitbucket) Kind() string { return "bitbucket" }

func (b *bitbucket) repoPath() string {
	return "/repositories/" + pathEscape(b.workspace) + "/" + pathEscape(b.slug)
}

func (b *bitbucket) GitAuth() *gitrepo.Auth {
	if b.gitPass == "" {
		return nil
	}
	return &gitrepo.Auth{Username: b.gitUser, Password: b.gitPass}
}

type bbLink struct {
	Href string `json:"href"`
	Name string `json:"name"`
}

func (b *bitbucket) Repository(ctx context.Context) (*Repository, error) {
	var r struct {
		MainBranch struct {
			Name string `json:"name"`
		} `json:"mainbranch"`
		Links struct {
			Clone []bbLink `json:"clone"`
			HTML  bbLink   `json:"html"`
		} `json:"links"`
	}
	if _, err := b.c.do(ctx, "GET", b.repoPath(), nil, &r); err != nil {
		return nil, err
	}
	info := &Repository{DefaultBranch: r.MainBranch.Name, WebURL: r.Links.HTML.Href}
	for _, l := range r.Links.Clone {
		if l.Name == "https" {
			// The API embeds the caller's username; credentials come from GitAuth.
			if u, err := url.Parse(l.Href); err == nil {
				u.User = nil
				info.CloneURL = u.String()
			}
		}
	}
	return info, nil
}

type bbPR struct {
	ID           int    `json:"id"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	State        string `json:"state"`
	Draft        bool   `json:"draft"`
	CommentCount int    `json:"comment_count"`
	Source       struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
	} `json:"source"`
	Destination struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
	} `json:"destination"`
	MergeCommit *struct {
		Hash string `json:"hash"`
	} `json:"merge_commit"`
	Links struct {
		HTML bbLink `json:"html"`
	} `json:"links"`
}

type bbPage[T any] struct {
	Values []T    `json:"values"`
	Next   string `json:"next"`
}

// list follows Bitbucket's "next" links until max values were read (0 = all).
func bbList[T any](ctx context.Context, c *client, path string, max int) ([]T, error) {
	var out []T
	for next := path; next != "" && (max <= 0 || len(out) < max); {
		var p bbPage[T]
		if _, err := c.do(ctx, "GET", next, nil, &p); err != nil {
			return nil, err
		}
		out = append(out, p.Values...)
		next = p.Next
	}
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out, nil
}

func (b *bitbucket) toPR(ctx context.Context, p bbPR) (*PullRequest, error) {
	body, labels := bbDecodeBody(p.Description)
	pr := &PullRequest{
		Number: p.ID, Title: p.Title, Body: body, Labels: labels,
		HeadBranch: p.Source.Branch.Name, BaseBranch: p.Destination.Branch.Name,
		Merged: p.State == "MERGED", URL: p.Links.HTML.Href, Draft: p.Draft,
	}
	if p.MergeCommit != nil {
		pr.MergeCommitSHA = p.MergeCommit.Hash
	}
	if labels != nil && p.State != "OPEN" && p.CommentCount > 0 {
		var err error
		if pr.Labels, err = b.replayLabelComments(ctx, p.ID, labels); err != nil {
			return nil, err
		}
	}
	return pr, nil
}

func (b *bitbucket) prPath(n int) string { return fmt.Sprintf("%s/pullrequests/%d", b.repoPath(), n) }

func bbQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func (b *bitbucket) FindOpenPR(ctx context.Context, head, base string) (*PullRequest, error) {
	q := fmt.Sprintf(`source.branch.name=%s AND destination.branch.name=%s AND state="OPEN"`, bbQuote(head), bbQuote(base))
	// Drafts are left out of pull request listings unless asked for.
	for _, extra := range []string{"", " AND draft=true"} {
		prs, err := bbList[bbPR](ctx, b.c, b.repoPath()+"/pullrequests?q="+url.QueryEscape(q+extra), 1)
		if err != nil {
			return nil, err
		}
		if len(prs) > 0 {
			return b.toPR(ctx, prs[0])
		}
	}
	return nil, nil
}

func (b *bitbucket) GetPR(ctx context.Context, n int) (*PullRequest, error) {
	var p bbPR
	if _, err := b.c.do(ctx, "GET", b.prPath(n), nil, &p); err != nil {
		return nil, err
	}
	return b.toPR(ctx, p)
}

func (b *bitbucket) CreatePR(ctx context.Context, in PRInput) (*PullRequest, error) {
	body := map[string]any{
		"title":               in.Title,
		"description":         bbEncodeBody(in.Body, in.Labels),
		"source":              map[string]any{"branch": map[string]string{"name": in.Head}},
		"destination":         map[string]any{"branch": map[string]string{"name": in.Base}},
		"close_source_branch": false,
		"draft":               in.Draft,
	}
	var p bbPR
	if _, err := b.c.do(ctx, "POST", b.repoPath()+"/pullrequests", body, &p); err != nil {
		return nil, err
	}
	return b.toPR(ctx, p)
}

func (b *bitbucket) UpdatePR(ctx context.Context, n int, in PRInput) (*PullRequest, error) {
	labels := in.Labels
	if labels == nil {
		cur, err := b.GetPR(ctx, n)
		if err != nil {
			return nil, err
		}
		labels = cur.Labels
	}
	body := map[string]any{"title": in.Title, "description": bbEncodeBody(in.Body, labels), "draft": in.Draft}
	var p bbPR
	if _, err := b.c.do(ctx, "PUT", b.prPath(n), body, &p); err != nil {
		return nil, err
	}
	return b.toPR(ctx, p)
}

func (b *bitbucket) ClosePR(ctx context.Context, n int) error {
	_, err := b.c.do(ctx, "POST", b.prPath(n)+"/decline", nil, nil)
	return err
}

func (b *bitbucket) ListMergedPRs(ctx context.Context, base string, limit int) ([]PullRequest, error) {
	q := url.QueryEscape(fmt.Sprintf(`state="MERGED" AND destination.branch.name=%s`, bbQuote(base)))
	prs, err := bbList[bbPR](ctx, b.c, b.repoPath()+"/pullrequests?state=MERGED&sort=-updated_on&pagelen=50&q="+q, limit)
	if err != nil {
		return nil, err
	}
	out := make([]PullRequest, 0, len(prs))
	for _, p := range prs {
		pr, err := b.toPR(ctx, p)
		if err != nil {
			return nil, err
		}
		out = append(out, *pr)
	}
	return out, nil
}

func (b *bitbucket) AddLabels(ctx context.Context, n int, labels []string) error {
	return b.changeLabels(ctx, n, labels, "added")
}

func (b *bitbucket) RemoveLabels(ctx context.Context, n int, labels []string) error {
	return b.changeLabels(ctx, n, labels, "removed")
}

func (b *bitbucket) changeLabels(ctx context.Context, n int, labels []string, verb string) error {
	if len(labels) == 0 {
		return nil
	}
	var p bbPR
	if _, err := b.c.do(ctx, "GET", b.prPath(n), nil, &p); err != nil {
		return err
	}
	if p.State == "OPEN" {
		body, cur := bbDecodeBody(p.Description)
		next := applyLabels(cur, labels, verb)
		_, err := b.c.do(ctx, "PUT", b.prPath(n), map[string]any{"title": p.Title, "description": bbEncodeBody(body, next), "draft": p.Draft}, nil)
		return err
	}
	// Merged and declined PRs cannot be edited: record the change as a comment.
	lines := make([]string, len(labels))
	for i, l := range labels {
		lines[i] = fmt.Sprintf("publish-please: %s label `%s`", verb, l)
	}
	return b.Comment(ctx, n, strings.Join(lines, "\n"))
}

var bbLabelEventRe = regexp.MustCompile("(?m)^publish-please: (added|removed) label `([^`]+)`\\s*$")

func (b *bitbucket) replayLabelComments(ctx context.Context, n int, labels []string) ([]string, error) {
	type comment struct {
		Content struct {
			Raw string `json:"raw"`
		} `json:"content"`
	}
	cs, err := bbList[comment](ctx, b.c, b.prPath(n)+"/comments?sort=created_on&pagelen=100", 0)
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		for _, m := range bbLabelEventRe.FindAllStringSubmatch(strings.ReplaceAll(c.Content.Raw, "\r\n", "\n"), -1) {
			labels = applyLabels(labels, []string{m[2]}, m[1])
		}
	}
	return labels, nil
}

func applyLabels(cur, change []string, verb string) []string {
	out := []string{}
	drop := map[string]bool{}
	for _, l := range change {
		drop[l] = true
	}
	for _, l := range cur {
		if !drop[l] {
			out = append(out, l)
		}
	}
	if verb == "added" {
		out = append(out, change...)
	}
	return out
}

func (b *bitbucket) Comment(ctx context.Context, n int, body string) error {
	_, err := b.c.do(ctx, "POST", b.prPath(n)+"/comments", map[string]any{"content": map[string]string{"raw": body}}, nil)
	return err
}

type bbTag struct {
	Name   string `json:"name"`
	Target struct {
		Hash string `json:"hash"`
	} `json:"target"`
}

func (b *bitbucket) CreateRelease(ctx context.Context, in ReleaseInput) (*Release, error) {
	// Bitbucket has no releases (and so no drafts or prereleases): tag only.
	msg := in.Name
	if in.Notes != "" {
		msg += "\n\n" + in.Notes
	}
	body := map[string]any{"name": in.Tag, "target": map[string]string{"hash": in.Target}, "message": msg}
	var t bbTag
	if _, err := b.c.do(ctx, "POST", b.repoPath()+"/refs/tags", body, &t); err != nil {
		return nil, err
	}
	return &Release{Tag: in.Tag, URL: b.tagURL(in.Tag)}, nil
}

func (b *bitbucket) ReleaseByTag(ctx context.Context, tag string) (*Release, error) {
	var t bbTag
	if _, err := b.c.do(ctx, "GET", b.repoPath()+"/refs/tags/"+pathEscape(tag), nil, &t); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &Release{Tag: tag, URL: b.tagURL(tag)}, nil
}

func (b *bitbucket) web() string { return webBase(b.o) + "/" + b.o.Repo }

func (b *bitbucket) tagURL(tag string) string { return b.web() + "/src/" + url.PathEscape(tag) + "/" }

func (b *bitbucket) CommitURL(sha string) string { return b.web() + "/commits/" + sha }

// CompareURL: Bitbucket puts the newer ref first, separated by a CR.
func (b *bitbucket) CompareURL(from, to string) string {
	return b.web() + "/branches/compare/" + url.PathEscape(to) + "%0D" + url.PathEscape(from) + "#diff"
}
func (b *bitbucket) IssueURL(n int) string { return fmt.Sprintf("%s/pull-requests/%d", b.web(), n) }
func (b *bitbucket) IssuePrefix() string   { return "#" }

const bbLabelsKey = "publish-please:labels "

var (
	htmlCommentRe = regexp.MustCompile(`(?m)^<!-- (.*[^ ]) -->$`)
	bbHiddenRe    = regexp.MustCompile(`(?m)^\[//\]: # \((.*)\)$`)
	bbLabelsRe    = regexp.MustCompile(`\n*\[//\]: # \(` + bbLabelsKey + `([A-Za-z0-9+/=]*)\)\s*$`)
)

// bbEncodeBody turns one-line HTML comments into hidden link reference
// definitions and appends the PR's labels as one more hidden line.
func bbEncodeBody(body string, labels []string) string {
	body = htmlCommentRe.ReplaceAllStringFunc(body, func(s string) string {
		inner := htmlCommentRe.FindStringSubmatch(s)[1]
		if strings.ContainsAny(inner, "()") {
			return s
		}
		return "[//]: # (" + inner + ")"
	})
	if labels == nil {
		labels = []string{}
	}
	j, _ := json.Marshal(labels)
	return strings.TrimRight(body, "\n") + "\n\n[//]: # (" + bbLabelsKey + base64.StdEncoding.EncodeToString(j) + ")"
}

// bbDecodeBody reverses bbEncodeBody. labels is nil when the description has
// no labels line (the PR was not opened by publish-please).
func bbDecodeBody(desc string) (string, []string) {
	desc = strings.ReplaceAll(desc, "\r\n", "\n")
	var labels []string
	if m := bbLabelsRe.FindStringSubmatch(desc); m != nil {
		labels = []string{}
		if raw, err := base64.StdEncoding.DecodeString(m[1]); err == nil {
			_ = json.Unmarshal(raw, &labels)
		}
		desc = desc[:len(desc)-len(m[0])]
	}
	desc = bbHiddenRe.ReplaceAllString(desc, "<!-- $1 -->")
	return desc, labels
}
