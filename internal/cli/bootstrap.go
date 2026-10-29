package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/MdTanvirHossainTusher/publish-please/internal/config"
	"github.com/MdTanvirHossainTusher/publish-please/internal/forge"
	"github.com/MdTanvirHossainTusher/publish-please/internal/gitrepo"
	"github.com/MdTanvirHossainTusher/publish-please/internal/semver"
	"github.com/MdTanvirHossainTusher/publish-please/internal/strategy"
)

const (
	bootstrapBranch = "publish-please--bootstrap"
	schemaURL       = "https://raw.githubusercontent.com/googleapis/release-please/main/schemas/config.json"
)

// bootstrap opens a PR adding a config file and versions manifest.
func (a *app) bootstrap(ctx context.Context) error {
	o := a.o
	if o.releaseType == "" {
		return errors.New("bootstrap needs --release-type")
	}
	if _, err := strategy.Get(o.releaseType); err != nil {
		return err
	}
	ref := gitrepo.BranchRef(o.target)
	head := a.repo.Resolve(ctx, ref)
	if head == "" {
		return fmt.Errorf("branch %q not found", o.target)
	}
	cfgName := o.configFile
	if cfgName == "" {
		cfgName = "release-please-config.json"
	}
	manName := o.manifestFile
	if manName == "" {
		manName = ".release-please-manifest.json"
	}
	if _, ok, _ := a.repo.ReadFile(ctx, ref, cfgName); ok {
		return fmt.Errorf("%s already exists on %s", cfgName, o.target)
	}

	path := config.NormalisePath(o.path)
	pkg := map[string]any{"release-type": o.releaseType}
	if o.packageName != "" {
		pkg["package-name"] = o.packageName
	}
	if o.component != "" {
		pkg["component"] = o.component
	}
	cfg := map[string]any{"$schema": schemaURL, "packages": map[string]any{path: pkg}}
	cfgJSON, _ := json.MarshalIndent(cfg, "", "  ")

	version := o.currentVersion
	if version == "" {
		version = a.latestTagVersion(ctx)
	}
	versions := map[string]string{}
	if version != "" {
		if _, err := semver.Parse(version); err != nil {
			return fmt.Errorf("--current-version: %w", err)
		}
		versions[path] = version
	}
	manJSON, _ := json.MarshalIndent(versions, "", "  ")

	files := map[string]string{cfgName: string(cfgJSON) + "\n", manName: string(manJSON) + "\n"}
	title := "chore: bootstrap releases for path: " + path
	if o.dryRun {
		fmt.Fprintf(a.stdout, "Would open %q on branch %s with:\n\n--- %s\n%s\n--- %s\n%s\n", title, bootstrapBranch, cfgName, files[cfgName], manName, files[manName])
		return nil
	}
	commit, err := a.repo.Commit(ctx, head, files, title, gitrepo.Identity{Name: o.userName, Email: o.userEmail})
	if err != nil {
		return err
	}
	if err := a.repo.Push(ctx, commit, bootstrapBranch); err != nil {
		return err
	}
	body := "This adds the configuration publish-please (and release-please) uses.\n\n" +
		"- `" + cfgName + "` describes the packages to release.\n" +
		"- `" + manName + "` records the last released version of each package.\n"
	if version != "" {
		body += "\nThe current version was set to `" + version + "`; only commits after its tag will be released.\n"
	} else {
		body += "\nNo previous release was found, so the first release will use the initial version (1.0.0 unless `initial-version` is set).\n"
	}
	existing, err := a.forge.FindOpenPR(ctx, bootstrapBranch, o.target)
	if err != nil {
		return err
	}
	in := forge.PRInput{Title: title, Body: body, Head: bootstrapBranch, Base: o.target}
	var pr *forge.PullRequest
	if existing != nil {
		pr, err = a.forge.UpdatePR(ctx, existing.Number, in)
	} else {
		pr, err = a.forge.CreatePR(ctx, in)
	}
	if err != nil {
		return err
	}
	a.logf("bootstrap PR: %s", pr.URL)
	return nil
}

// latestTagVersion returns the highest vX.Y.Z tag, or "".
func (a *app) latestTagVersion(ctx context.Context) string {
	tags, err := a.repo.Tags(ctx)
	if err != nil {
		return ""
	}
	var vs []semver.Version
	for t := range tags {
		if v, err := semver.Parse(t); err == nil {
			vs = append(vs, v)
		}
	}
	if len(vs) == 0 {
		return ""
	}
	sort.Slice(vs, func(i, j int) bool { return semver.Compare(vs[i], vs[j]) > 0 })
	return vs[0].String()
}
