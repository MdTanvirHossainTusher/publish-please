package manifest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/MdTanvirHossainTusher/publish-please/internal/config"
	"github.com/MdTanvirHossainTusher/publish-please/internal/forge"
	"github.com/MdTanvirHossainTusher/publish-please/internal/gitrepo"
)

// releaseData is embedded in the PR body so the release step knows exactly
// what to tag after the PR is merged.
type releaseData struct {
	V        int           `json:"v"`
	Releases []releaseItem `json:"releases"`
}

type releaseItem struct {
	Path      string `json:"path"`
	Component string `json:"component,omitempty"`
	Version   string `json:"version"`
	Tag       string `json:"tag"`
	Changelog string `json:"changelog,omitempty"`
	// Notes are stored only when there is no changelog file to read them from.
	Notes string `json:"notes,omitempty"`
}

const dataMarker = "<!-- publish-please:release-data "

var dataRe = regexp.MustCompile(`<!-- publish-please:release-data ([A-Za-z0-9+/=]+) -->`)

func encodeData(d releaseData) string {
	b, _ := json.Marshal(d)
	return dataMarker + base64.StdEncoding.EncodeToString(b) + " -->"
}

func decodeData(body string) (*releaseData, bool) {
	m := dataRe.FindStringSubmatch(body)
	if m == nil {
		return nil, false
	}
	b, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		return nil, false
	}
	var d releaseData
	if json.Unmarshal(b, &d) != nil || len(d.Releases) == 0 {
		return nil, false
	}
	return &d, true
}

// group is one release pull request.
type group struct {
	branch string
	cands  []*Candidate
}

func (r *Runner) branchFor(c *Candidate) string {
	b := BranchPrefix + r.Target
	if c == nil {
		return b
	}
	name := c.Component
	if name == "" {
		name = strings.ReplaceAll(c.Pkg.Path, "/", "-")
	}
	return b + "--components--" + name
}

func (r *Runner) groups(cands []*Candidate) []group {
	if !r.Config.SeparatePullRequests {
		if len(cands) == 0 {
			return nil
		}
		return []group{{branch: r.branchFor(nil), cands: cands}}
	}
	gs := make([]group, 0, len(cands))
	for _, c := range cands {
		gs = append(gs, group{branch: r.branchFor(c), cands: []*Candidate{c}})
	}
	return gs
}

func (r *Runner) title(g group) string {
	if len(g.cands) == 1 {
		c := g.cands[0]
		pattern := c.Pkg.PullRequestTitlePattern
		if pattern == "" {
			pattern = config.DefaultTitlePattern
		}
		comp := ""
		if c.Name != "" {
			comp = " " + c.Name
		}
		return strings.NewReplacer(
			"${scope}", "("+r.Target+")",
			"${component}", comp,
			"${version}", c.Version.String(),
			"${branch}", r.Target,
		).Replace(pattern)
	}
	pattern := r.Config.GroupPullRequestTitlePattern
	if pattern == "" {
		pattern = config.DefaultGroupTitle
	}
	return strings.NewReplacer("${branch}", r.Target, "${scope}", "("+r.Target+")").Replace(pattern)
}

// maxBody keeps bodies under GitHub's 65536 character limit.
const maxBody = 60000

func (r *Runner) body(g group) string {
	header, footer := config.DefaultPRHeader, config.DefaultPRFooter
	if p := g.cands[0].Pkg; p.PullRequestHeader != "" {
		header = p.PullRequestHeader
	}
	if p := g.cands[0].Pkg; p.PullRequestFooter != "" {
		footer = p.PullRequestFooter
	}
	data := releaseData{V: 1}
	var notes strings.Builder
	for _, c := range g.cands {
		item := releaseItem{Path: c.Pkg.Path, Component: c.Component, Version: c.Version.String(), Tag: c.Tag}
		if c.Pkg.SkipChangelog {
			item.Notes = c.Notes
		} else {
			item.Changelog = c.Pkg.ChangelogFile()
		}
		data.Releases = append(data.Releases, item)
		if len(g.cands) == 1 {
			notes.WriteString(c.Notes)
			continue
		}
		name := orStr(c.Name, c.Pkg.Path)
		fmt.Fprintf(&notes, "<details><summary>%s: %s</summary>\n\n%s\n</details>\n\n", name, c.Version, strings.TrimRight(c.Notes, "\n"))
	}
	meta := encodeData(data)
	n := notes.String()
	build := func(n string) string {
		return header + "\n---\n\n\n" + strings.TrimRight(n, "\n") + "\n\n---\n" + footer + "\n\n" + meta
	}
	out := build(n)
	if len(out) > maxBody {
		out = build(":warning: The release notes are too long to show here; see the changelog in this pull request.")
	}
	return out
}

// pendingReleases returns merged release PRs that have not been released yet.
func (r *Runner) pendingReleases(ctx context.Context) ([]forge.PullRequest, error) {
	limit := r.Config.ReleaseSearchDepth
	if limit > 200 {
		limit = 200
	}
	prs, err := r.Forge.ListMergedPRs(ctx, r.Target, limit)
	if err != nil {
		return nil, err
	}
	var out []forge.PullRequest
	for _, p := range prs {
		if !strings.HasPrefix(p.HeadBranch, BranchPrefix+r.Target) {
			continue
		}
		if len(r.Config.Labels) > 0 && !p.HasLabel(r.Config.Labels[0]) {
			continue
		}
		if _, ok := decodeData(p.Body); !ok {
			continue
		}
		out = append(out, p)
	}
	// oldest first so tags are created in order
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out, nil
}

// PRResult describes a release PR that was created or updated.
type PRResult struct {
	PR         *forge.PullRequest
	Candidates []*Candidate
	Created    bool
	Updated    bool
}

// CreatePullRequests opens or refreshes release PRs. It returns nothing to
// do when no package has releasable changes.
func (r *Runner) CreatePullRequests(ctx context.Context) ([]PRResult, error) {
	if r.Forge != nil && !r.DryRun {
		pending, err := r.pendingReleases(ctx)
		if err != nil {
			return nil, err
		}
		if len(pending) > 0 {
			nums := make([]string, len(pending))
			for i, p := range pending {
				nums[i] = fmt.Sprintf("#%d", p.Number)
			}
			r.logf("merged release PR(s) %s are not released yet; run `publish-please release` first. Skipping release PR.", strings.Join(nums, ", "))
			return nil, nil
		}
	}
	cands, err := r.Plan(ctx)
	if err != nil {
		return nil, err
	}
	gs := r.groups(cands)
	if len(gs) == 0 {
		r.logf("no releasable changes")
		if !r.Config.SeparatePullRequests {
			if err := r.closeStale(ctx, r.branchFor(nil)); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	var results []PRResult
	for _, g := range gs {
		res, err := r.openPR(ctx, g)
		if err != nil {
			return nil, err
		}
		if res != nil {
			results = append(results, *res)
		}
	}
	return results, nil
}

func (r *Runner) closeStale(ctx context.Context, branch string) error {
	if r.Forge == nil || r.DryRun {
		return nil
	}
	pr, err := r.Forge.FindOpenPR(ctx, branch, r.Target)
	if err != nil || pr == nil {
		return err
	}
	r.logf("closing release PR #%d: nothing left to release", pr.Number)
	_ = r.Forge.Comment(ctx, pr.Number, ":robot: There are no longer any releasable changes, so this release PR has been closed.")
	return r.Forge.ClosePR(ctx, pr.Number)
}

func (r *Runner) openPR(ctx context.Context, g group) (*PRResult, error) {
	files, err := r.Render(ctx, g.cands)
	if err != nil {
		return nil, err
	}
	title := r.title(g)
	body := r.body(g)
	msg := title
	if r.Config.Signoff != "" {
		msg += "\n\nSigned-off-by: " + r.Config.Signoff
	}
	draft := false
	for _, c := range g.cands {
		draft = draft || c.Pkg.DraftPullRequest
	}

	if r.DryRun {
		r.out("Would open release PR on branch %s:\n  title: %s\n", g.branch, title)
		for _, c := range g.cands {
			r.out("  %s -> %s (tag %s)\n", orStr(c.Name, c.Pkg.Path), c.Version, c.Tag)
		}
		paths := make([]string, 0, len(files))
		for p := range files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		r.out("  files:\n")
		for _, p := range paths {
			r.out("    %s\n", p)
		}
		r.out("\n%s\n\n", body)
		return &PRResult{Candidates: g.cands}, nil
	}

	commit, err := r.Repo.Commit(ctx, r.head, files, msg, r.Author)
	if err != nil {
		return nil, err
	}
	existing, err := r.Forge.FindOpenPR(ctx, g.branch, r.Target)
	if err != nil {
		return nil, err
	}

	push := true
	if existing != nil && !r.Config.AlwaysUpdate {
		if ok, _ := r.Repo.FetchBranch(ctx, g.branch); ok {
			old := r.Repo.Resolve(ctx, gitrepo.BranchRef(g.branch))
			if old != "" && r.Repo.Resolve(ctx, old+"^") == r.head && r.Repo.TreeOf(ctx, old) == r.Repo.TreeOf(ctx, commit) {
				push = false
			}
		}
	}
	if push {
		r.logf("pushing %s to %s", commit[:7], g.branch)
		if err := r.Repo.Push(ctx, commit, g.branch); err != nil {
			return nil, err
		}
	} else {
		r.logf("branch %s is already up to date", g.branch)
	}

	labels := append(append([]string{}, r.Config.Labels...), r.Config.ExtraLabels...)
	in := forge.PRInput{Title: title, Body: body, Head: g.branch, Base: r.Target, Labels: labels, Draft: draft}
	if existing == nil {
		pr, err := r.Forge.CreatePR(ctx, in)
		if err != nil {
			return nil, err
		}
		r.logf("opened release PR #%d: %s", pr.Number, pr.URL)
		return &PRResult{PR: pr, Candidates: g.cands, Created: true}, nil
	}
	pr := existing
	if strings.TrimPrefix(strings.TrimPrefix(existing.Title, "WIP: "), "Draft: ") != title || strings.TrimSpace(existing.Body) != strings.TrimSpace(body) {
		if pr, err = r.Forge.UpdatePR(ctx, existing.Number, in); err != nil {
			return nil, err
		}
	}
	var missing []string
	for _, l := range labels {
		if !existing.HasLabel(l) {
			missing = append(missing, l)
		}
	}
	if err := r.Forge.AddLabels(ctx, existing.Number, missing); err != nil {
		return nil, err
	}
	updated := push || pr != existing || len(missing) > 0
	if updated {
		r.logf("updated release PR #%d: %s", pr.Number, pr.URL)
	} else {
		r.logf("release PR #%d is up to date: %s", pr.Number, pr.URL)
	}
	return &PRResult{PR: pr, Candidates: g.cands, Updated: updated}, nil
}

func (r *Runner) out(format string, a ...any) {
	if r.Out != nil {
		r.Out(format, a...)
	}
}
