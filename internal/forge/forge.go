// Package forge talks to code hosting APIs (Gitea/Forgejo, GitHub, GitLab).
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MdTanvirHossainTusher/publish-please/internal/gitrepo"
)

// PullRequest is a pull request (GitHub/Gitea) or merge request (GitLab).
type PullRequest struct {
	Number         int
	Title          string
	Body           string
	HeadBranch     string
	BaseBranch     string
	Labels         []string
	Merged         bool
	MergeCommitSHA string
	URL            string
	Draft          bool
}

// HasLabel reports whether the PR carries label l.
func (p PullRequest) HasLabel(l string) bool {
	for _, x := range p.Labels {
		if x == l {
			return true
		}
	}
	return false
}

// PRInput is used to create or update a pull request.
type PRInput struct {
	Title  string
	Body   string
	Head   string
	Base   string
	Labels []string
	Draft  bool
}

// ReleaseInput creates a forge release (and its tag).
type ReleaseInput struct {
	Tag        string
	Target     string // commit SHA
	Name       string
	Notes      string
	Draft      bool
	Prerelease bool
}

// Release is a created release.
type Release struct {
	ID        int64
	Tag       string
	URL       string
	UploadURL string
	Draft     bool
}

// Repository is basic repository information.
type Repository struct {
	DefaultBranch string
	CloneURL      string
	WebURL        string
}

// Forge is the API surface publish-please needs.
type Forge interface {
	Kind() string
	Repository(ctx context.Context) (*Repository, error)
	// GitAuth returns credentials for git over HTTPS.
	GitAuth() *gitrepo.Auth

	FindOpenPR(ctx context.Context, head, base string) (*PullRequest, error)
	GetPR(ctx context.Context, number int) (*PullRequest, error)
	CreatePR(ctx context.Context, in PRInput) (*PullRequest, error)
	UpdatePR(ctx context.Context, number int, in PRInput) (*PullRequest, error)
	ClosePR(ctx context.Context, number int) error
	// ListMergedPRs returns recently merged PRs into base, newest first.
	ListMergedPRs(ctx context.Context, base string, limit int) ([]PullRequest, error)
	AddLabels(ctx context.Context, number int, labels []string) error
	RemoveLabels(ctx context.Context, number int, labels []string) error
	Comment(ctx context.Context, number int, body string) error

	CreateRelease(ctx context.Context, in ReleaseInput) (*Release, error)
	// ReleaseByTag returns nil, nil when no release exists for tag.
	ReleaseByTag(ctx context.Context, tag string) (*Release, error)

	CommitURL(sha string) string
	CompareURL(from, to string) string
	IssueURL(n int) string
	// IssuePrefix is how PR references look in commit messages ("#" or "!").
	IssuePrefix() string
}

// Options configures a forge client.
type Options struct {
	Kind   string // gitea, forgejo, github, gitlab
	APIURL string // API base, e.g. https://gitea.example.com/api/v1
	WebURL string // web base, e.g. https://gitea.example.com
	Repo   string // owner/name (GitLab: group/subgroup/name)
	Token  string
	// ChangelogHost overrides WebURL for links in notes.
	ChangelogHost string
	HTTPClient    *http.Client
}

// New creates the forge client for o.Kind.
func New(o Options) (Forge, error) {
	if o.Repo == "" || !strings.Contains(o.Repo, "/") {
		return nil, fmt.Errorf("repository must be owner/name, got %q", o.Repo)
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	o.APIURL = strings.TrimRight(o.APIURL, "/")
	o.WebURL = strings.TrimRight(o.WebURL, "/")
	switch strings.ToLower(o.Kind) {
	case "gitea", "forgejo", "codeberg":
		return newGitea(o)
	case "github":
		return newGitHub(o)
	case "gitlab":
		return newGitLab(o)
	}
	return nil, fmt.Errorf("unknown forge %q (want gitea, forgejo, github or gitlab)", o.Kind)
}

// APIError is a non-2xx response.
type APIError struct {
	Status int
	Method string
	URL    string
	Body   string
}

func (e *APIError) Error() string {
	b := e.Body
	if len(b) > 500 {
		b = b[:500] + "..."
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.URL, e.Status, strings.TrimSpace(b))
}

// IsNotFound reports whether err is an HTTP 404.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

type client struct {
	http    *http.Client
	base    string
	headers map[string]string
}

// do sends a JSON request. out may be nil. It returns the response headers.
func (c *client) do(ctx context.Context, method, path string, in, out any) (http.Header, error) {
	u := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		u = c.base + path
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
			if in != nil {
				b, _ := json.Marshal(in)
				body = bytes.NewReader(b)
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, u, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "publish-please")
		if in != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			lastErr = &APIError{Status: resp.StatusCode, Method: method, URL: redact(u), Body: string(data)}
			continue
		}
		if resp.StatusCode >= 300 {
			return resp.Header, &APIError{Status: resp.StatusCode, Method: method, URL: redact(u), Body: string(data)}
		}
		if out != nil && len(bytes.TrimSpace(data)) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return resp.Header, fmt.Errorf("%s %s: decode: %w", method, redact(u), err)
			}
		}
		return resp.Header, nil
	}
	return nil, lastErr
}

func redact(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return u
	}
	q := p.Query()
	for _, k := range []string{"token", "access_token", "private_token"} {
		if q.Has(k) {
			q.Set(k, "***")
		}
	}
	p.RawQuery = q.Encode()
	return p.String()
}

func pathEscape(s string) string { return url.PathEscape(s) }

func splitRepo(r string) (string, string) {
	i := strings.LastIndex(r, "/")
	return r[:i], r[i+1:]
}

func webBase(o Options) string {
	if o.ChangelogHost != "" {
		return strings.TrimRight(o.ChangelogHost, "/")
	}
	return o.WebURL
}
