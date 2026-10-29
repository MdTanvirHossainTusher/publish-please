package manifest

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/MdTanvirHossainTusher/publish-please/internal/config"
	"github.com/MdTanvirHossainTusher/publish-please/internal/forge"
	"github.com/MdTanvirHossainTusher/publish-please/internal/semver"
)

// CreatedRelease is a release made (or found already made) by this run.
type CreatedRelease struct {
	Path      string
	Component string
	Version   semver.Version
	Tag       string
	SHA       string
	URL       string
	UploadURL string
	PR        int
	Draft     bool
	Skipped   bool // skip-release / skip-github-release
}

// CreateReleases tags and releases every merged, not yet released, release PR.
func (r *Runner) CreateReleases(ctx context.Context) ([]CreatedRelease, error) {
	pending, err := r.pendingReleases(ctx)
	if err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		r.logf("no merged release PRs waiting for a release")
	}
	var out []CreatedRelease
	for _, pr := range pending {
		data, _ := decodeData(pr.Body)
		sha := pr.MergeCommitSHA
		if sha == "" {
			r.logf("PR #%d has no merge commit SHA, using %s head", pr.Number, r.Target)
			sha = r.head
		}
		if r.Repo.Resolve(ctx, sha) == "" {
			return nil, fmt.Errorf("merge commit %s of PR #%d is not on %s", sha, pr.Number, r.Target)
		}
		var urls []string
		for _, item := range data.Releases {
			rel, err := r.releaseOne(ctx, pr, item, sha)
			if err != nil {
				return nil, fmt.Errorf("PR #%d, %s: %w", pr.Number, item.Tag, err)
			}
			out = append(out, rel)
			if rel.URL != "" {
				urls = append(urls, rel.URL)
			}
		}
		if r.DryRun {
			continue
		}
		if len(urls) > 0 {
			msg := ":robot: Release is at " + strings.Join(urls, ", ") + " :sunflower:"
			if err := r.Forge.Comment(ctx, pr.Number, msg); err != nil {
				r.logf("could not comment on PR #%d: %v", pr.Number, err)
			}
		}
		if err := r.Forge.RemoveLabels(ctx, pr.Number, r.Config.Labels); err != nil {
			return nil, err
		}
		if err := r.Forge.AddLabels(ctx, pr.Number, r.Config.ReleaseLabels); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *Runner) releaseOne(ctx context.Context, pr forge.PullRequest, item releaseItem, sha string) (CreatedRelease, error) {
	v, err := semver.Parse(item.Version)
	if err != nil {
		return CreatedRelease{}, err
	}
	res := CreatedRelease{Path: item.Path, Component: item.Component, Version: v, Tag: item.Tag, SHA: sha, PR: pr.Number}
	pkg := r.Config.Packages[config.NormalisePath(item.Path)]
	if pkg == nil {
		pkg = &config.Package{Path: item.Path}
	}
	if pkg.SkipsRelease() {
		r.logf("%s: release creation is disabled (skip-github-release)", item.Tag)
		res.Skipped = true
		return res, nil
	}
	if existing, err := r.Forge.ReleaseByTag(ctx, item.Tag); err != nil {
		return res, err
	} else if existing != nil {
		r.logf("release %s already exists: %s", item.Tag, existing.URL)
		res.URL, res.UploadURL, res.Draft = existing.URL, existing.UploadURL, existing.Draft
		return res, nil
	}
	notes := item.Notes
	if notes == "" && item.Changelog != "" {
		content, ok, err := r.Repo.ReadFile(ctx, sha, item.Changelog)
		if err != nil {
			return res, err
		}
		if ok {
			notes = ExtractNotes(content, item.Version)
		}
	}
	if notes == "" {
		notes = "Release " + item.Version
	}
	name := "v" + item.Version
	if item.Component != "" {
		name = item.Component + ": v" + item.Version
	}
	in := forge.ReleaseInput{
		Tag: item.Tag, Target: sha, Name: name, Notes: notes,
		Draft: pkg.Draft, Prerelease: v.IsPrerelease() || pkg.Prerelease,
	}
	if r.DryRun {
		r.out("Would create release %s at %s (PR #%d)\n%s\n", item.Tag, sha[:7], pr.Number, notes)
		return res, nil
	}
	rel, err := r.Forge.CreateRelease(ctx, in)
	if err != nil {
		return res, err
	}
	r.logf("created release %s: %s", item.Tag, rel.URL)
	res.URL, res.UploadURL, res.Draft = rel.URL, rel.UploadURL, rel.Draft
	return res, nil
}

// ExtractNotes returns the changelog entry for version (heading included),
// up to the next entry.
func ExtractNotes(changelog, version string) string {
	lines := strings.Split(strings.ReplaceAll(changelog, "\r\n", "\n"), "\n")
	start := -1
	for i, l := range lines {
		if !strings.HasPrefix(l, "## ") && !strings.HasPrefix(l, "### [") {
			continue
		}
		if start >= 0 {
			if strings.HasPrefix(l, "## ") || looksLikeVersion(l) {
				return strings.TrimSpace(strings.Join(lines[start:i], "\n")) + "\n"
			}
			continue
		}
		if strings.Contains(l, "["+version+"]") || strings.Contains(l, " "+version+" ") || strings.HasSuffix(l, " "+version) || strings.Contains(l, " v"+version+" ") {
			start = i
		}
	}
	if start >= 0 {
		return strings.TrimSpace(strings.Join(lines[start:], "\n")) + "\n"
	}
	return ""
}

var semverInHeading = regexp.MustCompile(`^###? \[?v?\d+\.\d+\.\d+`)

func looksLikeVersion(l string) bool {
	return semverInHeading.MatchString(l)
}
