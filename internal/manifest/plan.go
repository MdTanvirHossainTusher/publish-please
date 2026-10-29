// Package manifest is the release engine: it plans releases for every
// configured package, opens release pull requests and creates releases.
package manifest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MdTanvirHossainTusher/publish-please/internal/changelog"
	"github.com/MdTanvirHossainTusher/publish-please/internal/commits"
	"github.com/MdTanvirHossainTusher/publish-please/internal/config"
	"github.com/MdTanvirHossainTusher/publish-please/internal/forge"
	"github.com/MdTanvirHossainTusher/publish-please/internal/gitrepo"
	"github.com/MdTanvirHossainTusher/publish-please/internal/semver"
	"github.com/MdTanvirHossainTusher/publish-please/internal/strategy"
	"github.com/MdTanvirHossainTusher/publish-please/internal/updaters"
	"github.com/MdTanvirHossainTusher/publish-please/internal/versioning"
)

// BranchPrefix starts every release branch name.
const BranchPrefix = "publish-please--branches--"

// Runner holds everything needed for one run.
type Runner struct {
	Forge  forge.Forge
	Repo   *gitrepo.Repo
	Target string
	Config *config.Manifest
	// Versions is the parsed versions manifest (path -> version).
	Versions map[string]string
	// ManifestFile is the versions manifest path to update; "" disables it.
	ManifestFile string
	DryRun       bool
	Author       gitrepo.Identity
	Now          func() time.Time
	Logf         func(format string, a ...any)
	// Out receives dry-run output.
	Out func(format string, a ...any)

	head    string
	tags    map[string]string
	prCache map[int]*forge.PullRequest
}

// Init must be called after the target branch has been fetched.
func (r *Runner) Init(ctx context.Context) error {
	r.head = r.Repo.Resolve(ctx, gitrepo.BranchRef(r.Target))
	if r.head == "" {
		return fmt.Errorf("branch %q not found on the remote", r.Target)
	}
	tags, err := r.Repo.Tags(ctx)
	if err != nil {
		return err
	}
	r.tags = tags
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Versions == nil {
		r.Versions = map[string]string{}
	}
	r.prCache = map[int]*forge.PullRequest{}
	return nil
}

// Head returns the target branch commit the run is based on.
func (r *Runner) Head() string { return r.head }

func (r *Runner) logf(format string, a ...any) {
	if r.Logf != nil {
		r.Logf(format, a...)
	}
}

// state is the computed release information for one package.
type state struct {
	pkg       *config.Package
	strat     strategy.Strategy
	component string
	name      string // display name
	prev      *semver.Version
	prevTag   string
	changes   []commits.Change
	// releasable is true when the commits warrant a release on their own.
	releasable bool
}

// Candidate is one package that will be released.
type Candidate struct {
	Pkg       *config.Package
	Component string
	// Name is for display: the component, else the detected package name, else the path.
	Name    string
	Prev    *semver.Version
	PrevTag string
	Version semver.Version
	Tag     string
	Changes []commits.Change
	Notes   string
	// DepNotes are extra changelog lines added by plugins (node-workspace).
	DepNotes []string
	// ExtraUpdates come from plugins and are applied after the strategy's.
	ExtraUpdates []updaters.FileUpdate
}

// files reads repository content at the target head.
type files struct {
	r   *Runner
	ref string
}

func (f files) Read(ctx context.Context, p string) (string, bool, error) {
	return f.r.Repo.ReadFile(ctx, f.ref, p)
}

func (f files) List(ctx context.Context, dir string) ([]string, error) {
	return f.r.Repo.ListFiles(ctx, f.ref, dir)
}

func (r *Runner) files() files { return files{r: r, ref: r.head} }

// TagName builds a release tag for a package.
func TagName(pkg *config.Package, component string, v semver.Version) string {
	prefix := ""
	if pkg.IncludeVInTag == nil || *pkg.IncludeVInTag {
		prefix = "v"
	}
	if component != "" && (pkg.IncludeComponentInTag == nil || *pkg.IncludeComponentInTag) {
		sep := pkg.TagSeparator
		if sep == "" {
			sep = "-"
		}
		return component + sep + prefix + v.String()
	}
	return prefix + v.String()
}

// parseTag returns the version encoded in tag for this package, if it matches.
func parseTag(pkg *config.Package, component, tag string) (semver.Version, bool) {
	rest := tag
	if component != "" && (pkg.IncludeComponentInTag == nil || *pkg.IncludeComponentInTag) {
		sep := pkg.TagSeparator
		if sep == "" {
			sep = "-"
		}
		if !strings.HasPrefix(rest, component+sep) {
			return semver.Version{}, false
		}
		rest = strings.TrimPrefix(rest, component+sep)
	}
	if !strings.HasPrefix(rest, "v") && pkg.IncludeVInTag != nil && *pkg.IncludeVInTag {
		return semver.Version{}, false
	}
	v, err := semver.Parse(rest)
	if err != nil {
		return semver.Version{}, false
	}
	return v, true
}

func (r *Runner) componentFor(ctx context.Context, pkg *config.Package, s strategy.Strategy) string {
	if pkg.Component != "" {
		return pkg.Component
	}
	if pkg.PackageName != "" {
		return strategy.ShortName(pkg.PackageName)
	}
	if pkg.IsRoot() {
		return ""
	}
	if n := s.DetectName(ctx, pkg, r.files()); n != "" {
		return strategy.ShortName(n)
	}
	parts := strings.Split(pkg.Path, "/")
	return parts[len(parts)-1]
}

// previous finds the last release of a package: its version, tag and commit.
func (r *Runner) previous(ctx context.Context, pkg *config.Package, component string) (*semver.Version, string, string) {
	var manifestVer *semver.Version
	if vs, ok := r.Versions[pkg.Path]; ok {
		if v, err := semver.Parse(vs); err == nil {
			manifestVer = &v
			tag := TagName(pkg, component, v)
			if sha := r.tags[tag]; sha != "" && r.Repo.IsAncestor(ctx, sha, r.head) {
				return manifestVer, tag, sha
			}
			r.logf("%s: tag %s for manifest version %s not found, searching tags", label(pkg), tag, vs)
		}
	}
	type tv struct {
		tag string
		v   semver.Version
	}
	var found []tv
	for tag := range r.tags {
		if v, ok := parseTag(pkg, component, tag); ok {
			found = append(found, tv{tag, v})
		}
	}
	sort.Slice(found, func(i, j int) bool { return semver.Compare(found[i].v, found[j].v) > 0 })
	for _, f := range found {
		if manifestVer != nil && semver.Compare(f.v, *manifestVer) > 0 {
			continue // never go past what the manifest says was released
		}
		sha := r.tags[f.tag]
		if r.Repo.IsAncestor(ctx, sha, r.head) {
			v := f.v
			return &v, f.tag, sha
		}
	}
	return manifestVer, "", ""
}

func label(pkg *config.Package) string {
	if pkg.IsRoot() {
		return "root package"
	}
	return "package " + pkg.Path
}

// changesFor parses commits, honouring BEGIN_COMMIT_OVERRIDE in merged PR bodies.
func (r *Runner) changesFor(ctx context.Context, cs []commits.Commit) []commits.Change {
	var out []commits.Change
	for _, c := range cs {
		if r.Forge != nil {
			if n := commits.MergedPR(c.Message); n > 0 {
				pr, ok := r.prCache[n]
				if !ok {
					p, err := r.Forge.GetPR(ctx, n)
					if err != nil {
						r.logf("could not load PR #%d for commit override: %v", n, err)
					}
					pr = p
					r.prCache[n] = p
				}
				if pr != nil {
					if text, ok := commits.Override(pr.Body); ok {
						ov := commits.ParseOverride(text)
						for i := range ov {
							ov[i].SHA, ov[i].Files = c.SHA, c.Files
						}
						out = append(out, ov...)
						continue
					}
				}
			}
		}
		out = append(out, commits.Parse(c)...)
	}
	return out
}

func (r *Runner) stateFor(ctx context.Context, pkg *config.Package) (*state, error) {
	s, err := strategy.Get(pkg.ReleaseType)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label(pkg), err)
	}
	st := &state{pkg: pkg, strat: s}
	st.component = r.componentFor(ctx, pkg, s)
	st.name = st.component
	if st.name == "" {
		st.name = strategy.ShortName(s.DetectName(ctx, pkg, r.files()))
	}
	var prevSHA string
	st.prev, st.prevTag, prevSHA = r.previous(ctx, pkg, st.component)

	from := prevSHA
	if from == "" {
		from = r.Config.LastReleaseSHA
	}
	if from == "" {
		from = r.Config.BootstrapSHA
	}
	opts := gitrepo.LogOptions{From: from, To: r.head}
	if from == "" {
		opts.Max = r.Config.CommitSearchDepth
	}
	if !pkg.IsRoot() {
		opts.Paths = []string{pkg.Path}
	}
	for _, e := range pkg.ExcludePaths {
		opts.Excludes = append(opts.Excludes, config.Join(pkg.Path, e))
	}
	cs, err := r.Repo.Log(ctx, opts)
	if err != nil {
		return nil, err
	}
	st.changes = r.changesFor(ctx, cs)
	st.releasable = changelog.Releasable(st.changes, pkg.Sections())
	r.logf("%s: %d commits since %s, releasable=%v", label(pkg), len(cs), orStr(st.prevTag, orStr(from, "the beginning")), st.releasable)
	return st, nil
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (r *Runner) nextVersion(st *state) (semver.Version, error) {
	p := st.pkg
	return versioning.Next(st.prev, st.changes, versioning.Options{
		Versioning:                p.Versioning,
		BumpMinorPreMajor:         p.BumpMinorPreMajor,
		BumpPatchForMinorPreMajor: p.BumpPatchForMinorPreMajor,
		Prerelease:                p.Prerelease,
		PrereleaseType:            p.PrereleaseType,
		ReleaseAs:                 p.ReleaseAs,
		InitialVersion:            p.InitialVersion,
	})
}

func (st *state) candidate(v semver.Version) *Candidate {
	return &Candidate{
		Pkg: st.pkg, Component: st.component, Name: st.name, Prev: st.prev, PrevTag: st.prevTag,
		Version: v, Tag: TagName(st.pkg, st.component, v), Changes: st.changes,
	}
}

// Plan computes the releases that should happen now, with their file changes.
func (r *Runner) Plan(ctx context.Context) ([]*Candidate, error) {
	states := map[string]*state{}
	var cands []*Candidate
	for _, p := range r.Config.Paths() {
		st, err := r.stateFor(ctx, r.Config.Packages[p])
		if err != nil {
			return nil, err
		}
		states[p] = st
		if !st.releasable {
			continue
		}
		v, err := r.nextVersion(st)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label(st.pkg), err)
		}
		if st.prev != nil && semver.Compare(v, *st.prev) <= 0 {
			r.logf("%s: next version %s is not above %s (stale release-as?), skipping", label(st.pkg), v, st.prev)
			continue
		}
		cands = append(cands, st.candidate(v))
	}

	sentence := false
	for _, pl := range r.Config.Plugins {
		var err error
		switch pl.Type {
		case "linked-versions":
			cands, err = r.linkedVersions(pl, states, cands)
		case "node-workspace":
			cands, err = r.nodeWorkspace(ctx, pl, states, cands)
		case "sentence-case":
			sentence = true
		default:
			r.logf("plugin %q is not supported and was ignored", pl.Type)
		}
		if err != nil {
			return nil, err
		}
	}

	for _, c := range cands {
		c.Tag = TagName(c.Pkg, c.Component, c.Version)
		c.Notes = r.renderNotes(c, sentence)
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Pkg.Path < cands[j].Pkg.Path })
	return cands, nil
}

func (r *Runner) renderNotes(c *Candidate, sentence bool) string {
	links := changelog.Links{}
	if r.Forge != nil {
		links = changelog.Links{
			Commit: r.Forge.CommitURL, Compare: r.Forge.CompareURL,
			Issue: r.Forge.IssueURL, IssuePrefix: r.Forge.IssuePrefix(),
		}
	}
	notes := changelog.Render(changelog.Input{
		Version: c.Version.String(), PreviousTag: c.PrevTag, CurrentTag: c.Tag,
		Date: r.Now(), DateFormat: c.Pkg.DateFormat, Changes: c.Changes,
		Sections: c.Pkg.Sections(), Links: links, SentenceCase: sentence,
	})
	if len(c.DepNotes) > 0 {
		notes = strings.TrimRight(notes, "\n") + "\n\n\n### Dependencies\n\n" + strings.Join(c.DepNotes, "\n") + "\n"
	}
	return notes
}

// Updates returns all file updates for a candidate, in order.
func (r *Runner) Updates(ctx context.Context, c *Candidate) ([]updaters.FileUpdate, error) {
	s, err := strategy.Get(c.Pkg.ReleaseType)
	if err != nil {
		return nil, err
	}
	var out []updaters.FileUpdate
	if !c.Pkg.SkipChangelog {
		out = append(out, updaters.FileUpdate{Path: c.Pkg.ChangelogFile(), Create: true, Updater: changelog.Prepend(c.Notes)})
	}
	us, err := s.Updates(ctx, c.Pkg, c.Version, r.files())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label(c.Pkg), err)
	}
	out = append(out, us...)
	extra, err := strategy.ExtraFileUpdates(ctx, c.Pkg, c.Version, r.files())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label(c.Pkg), err)
	}
	out = append(out, extra...)
	out = append(out, c.ExtraUpdates...)
	return out, nil
}

// Render applies updates on top of the target head and returns changed files.
func (r *Runner) Render(ctx context.Context, cands []*Candidate) (map[string]string, error) {
	changed := map[string]string{}
	exists := map[string]bool{}
	for _, c := range cands {
		us, err := r.Updates(ctx, c)
		if err != nil {
			return nil, err
		}
		for _, u := range us {
			content, ok := changed[u.Path]
			present := ok || exists[u.Path]
			if !ok {
				var err error
				content, present, err = r.Repo.ReadFile(ctx, r.head, u.Path)
				if err != nil {
					return nil, err
				}
			}
			if !present && !u.Create {
				continue // release-please skips missing files
			}
			out, err := u.Updater.Update(content, present)
			if err != nil {
				if u.Optional {
					continue
				}
				return nil, fmt.Errorf("updating %s: %w", u.Path, err)
			}
			if out != content || !present {
				changed[u.Path] = out
				exists[u.Path] = true
			}
		}
	}
	if r.ManifestFile != "" && len(cands) > 0 {
		vs := map[string]string{}
		for k, v := range r.Versions {
			vs[k] = v
		}
		for _, c := range cands {
			vs[c.Pkg.Path] = c.Version.String()
		}
		changed[r.ManifestFile] = encodeVersions(vs)
	}
	return changed, nil
}

// encodeVersions writes the manifest like release-please: sorted keys, 2-space indent.
func encodeVersions(vs map[string]string) string {
	keys := make([]string, 0, len(vs))
	for k := range vs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("{\n")
	for i, k := range keys {
		fmt.Fprintf(&b, "  %q: %q", k, vs[k])
		if i < len(keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return b.String()
}
