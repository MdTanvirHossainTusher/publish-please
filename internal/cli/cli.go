// Package cli implements the publish-please command line.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/MdTanvirHossainTusher/publish-please/internal/config"
	"github.com/MdTanvirHossainTusher/publish-please/internal/forge"
	"github.com/MdTanvirHossainTusher/publish-please/internal/gitrepo"
	"github.com/MdTanvirHossainTusher/publish-please/internal/manifest"
	"github.com/MdTanvirHossainTusher/publish-please/internal/strategy"
)

// Version is set at build time with -ldflags "-X .../cli.Version=x".
var Version = "dev"

const usage = `publish-please - release PRs, changelogs and releases from Conventional Commits.
Works with Gitea, Forgejo, GitHub and GitLab.

Usage:
  publish-please <command> [flags]

Commands:
  release-pr   Open or update the release pull request
  release      Tag and publish releases for merged release pull requests
  run          release, then release-pr (what you usually run in CI)
  bootstrap    Open a pull request that adds a config and manifest
  version      Print the version

Run "publish-please <command> -h" for flags.
`

type options struct {
	forge, serverURL, apiURL, repo, token, gitURL string
	target, configFile, manifestFile              string
	releaseType, packageName, path, component     string
	repoDir, userName, userEmail, changelogHost   string
	outputsFile                                   string
	dryRun, debug, jsonOut                        bool
	// bootstrap only
	currentVersion string
}

func (o *options) register(fs *flag.FlagSet, bootstrap bool) {
	fs.StringVar(&o.forge, "forge", env("PUBLISH_PLEASE_FORGE"), "gitea, forgejo, github or gitlab (auto-detected in CI)")
	fs.StringVar(&o.serverURL, "server-url", env("PUBLISH_PLEASE_SERVER_URL", "GITEA_SERVER_URL", "CI_SERVER_URL", "GITHUB_SERVER_URL"), "web URL of the forge, e.g. https://gitea.example.com")
	fs.StringVar(&o.apiURL, "api-url", env("PUBLISH_PLEASE_API_URL", "CI_API_V4_URL", "GITHUB_API_URL"), "API base URL (derived from --server-url when empty)")
	fs.StringVar(&o.repo, "repo", env("PUBLISH_PLEASE_REPO", "CI_PROJECT_PATH", "GITHUB_REPOSITORY"), "repository as owner/name")
	fs.StringVar(&o.token, "token", env("PUBLISH_PLEASE_TOKEN", "GITEA_TOKEN", "FORGEJO_TOKEN", "GITLAB_TOKEN", "GITHUB_TOKEN"), "API token (or PUBLISH_PLEASE_TOKEN)")
	fs.StringVar(&o.gitURL, "git-url", env("PUBLISH_PLEASE_GIT_URL"), "HTTPS clone URL, when the API reports an unreachable one")
	fs.StringVar(&o.target, "target-branch", env("PUBLISH_PLEASE_TARGET_BRANCH"), "branch to release from (default: repository default branch)")
	fs.StringVar(&o.configFile, "config-file", "", "config file (default: publish-please-config.json or release-please-config.json)")
	fs.StringVar(&o.manifestFile, "manifest-file", "", "versions manifest (default: .publish-please-manifest.json or .release-please-manifest.json)")
	fs.StringVar(&o.releaseType, "release-type", "", "release type when there is no config file ("+strings.Join(strategy.Types(), ", ")+")")
	fs.StringVar(&o.packageName, "package-name", "", "package name when there is no config file")
	fs.StringVar(&o.path, "path", ".", "package path when there is no config file")
	fs.StringVar(&o.component, "component", "", "component name when there is no config file")
	fs.StringVar(&o.repoDir, "repo-dir", "", "reuse this local clone instead of a temporary one (no working-tree changes are made)")
	fs.StringVar(&o.userName, "git-user-name", envOr("Publish Please", "PUBLISH_PLEASE_GIT_USER_NAME"), "commit author name")
	fs.StringVar(&o.userEmail, "git-user-email", envOr("publish-please@users.noreply.local", "PUBLISH_PLEASE_GIT_USER_EMAIL"), "commit author email")
	fs.StringVar(&o.changelogHost, "changelog-host", "", "base URL used for links in changelogs")
	fs.StringVar(&o.outputsFile, "outputs-file", os.Getenv("GITHUB_OUTPUT"), "write key=value outputs here (default: $GITHUB_OUTPUT)")
	fs.BoolVar(&o.dryRun, "dry-run", false, "show what would happen without changing anything")
	fs.BoolVar(&o.debug, "debug", false, "verbose logging")
	fs.BoolVar(&o.jsonOut, "json", false, "print the result as JSON on stdout")
	if bootstrap {
		fs.StringVar(&o.currentVersion, "current-version", "", "version already released (default: detected from tags)")
	}
}

func env(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func envOr(def string, keys ...string) string {
	if v := env(keys...); v != "" {
		return v
	}
	return def
}

// Main runs the CLI and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, Version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "release-pr", "release", "github-release", "run", "bootstrap":
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	var o options
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	o.register(fs, cmd == "bootstrap")
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	app := &app{o: o, stdout: stdout, stderr: stderr}
	if err := app.run(ctx, cmd); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

type app struct {
	o      options
	stdout io.Writer
	stderr io.Writer
	forge  forge.Forge
	repo   *gitrepo.Repo
}

func (a *app) logf(format string, args ...any) {
	fmt.Fprintf(a.stderr, "publish-please: "+format+"\n", args...)
}

func (a *app) debugf(format string, args ...any) {
	if a.o.debug {
		a.logf(format, args...)
	}
}

func (a *app) run(ctx context.Context, cmd string) error {
	if err := a.connect(ctx); err != nil {
		return err
	}
	defer a.repo.Close()
	switch cmd {
	case "bootstrap":
		return a.bootstrap(ctx)
	case "release-pr":
		r, err := a.runner(ctx)
		if err != nil {
			return err
		}
		prs, err := r.CreatePullRequests(ctx)
		if err != nil {
			return err
		}
		return a.report(nil, prs)
	case "release", "github-release":
		r, err := a.runner(ctx)
		if err != nil {
			return err
		}
		rels, err := r.CreateReleases(ctx)
		if err != nil {
			return err
		}
		return a.report(rels, nil)
	case "run":
		r, err := a.runner(ctx)
		if err != nil {
			return err
		}
		rels, err := r.CreateReleases(ctx)
		if err != nil {
			return err
		}
		if len(rels) > 0 && !a.o.dryRun {
			// New tags exist now; refresh so the next release PR starts after them.
			if r, err = a.runner(ctx); err != nil {
				return err
			}
		}
		prs, err := r.CreatePullRequests(ctx)
		if err != nil {
			return err
		}
		return a.report(rels, prs)
	}
	return nil
}

// connect builds the forge client and the local repository.
func (a *app) connect(ctx context.Context) error {
	o := &a.o
	kind, err := detectForge(ctx, o)
	if err != nil {
		return err
	}
	if o.repo == "" {
		return errors.New("--repo is required (owner/name)")
	}
	if kind == "github" && o.serverURL == "" {
		o.serverURL = "https://github.com"
	}
	if kind == "gitea" && o.apiURL != "" && !strings.Contains(o.apiURL, "/api/v1") {
		// GITHUB_API_URL in Gitea Actions already points at /api/v1, but be lenient.
		o.apiURL = strings.TrimRight(o.apiURL, "/") + "/api/v1"
	}
	f, err := forge.New(forge.Options{
		Kind: kind, APIURL: o.apiURL, WebURL: o.serverURL, Repo: o.repo,
		Token: o.token, ChangelogHost: o.changelogHost,
	})
	if err != nil {
		return err
	}
	a.forge = f
	a.debugf("forge=%s repo=%s", kind, o.repo)

	info, err := f.Repository(ctx)
	if err != nil {
		return fmt.Errorf("reading repository %s: %w", o.repo, err)
	}
	if o.target == "" {
		o.target = info.DefaultBranch
	}
	gitURL := o.gitURL
	if gitURL == "" {
		gitURL = info.CloneURL
		// Prefer the configured server host: in CI the API may report an
		// external ROOT_URL that the runner cannot reach, or the reverse.
		if o.serverURL != "" {
			gitURL = rebaseURL(gitURL, o.serverURL)
		}
	}
	if o.repoDir != "" {
		a.repo, err = gitrepo.Open(o.repoDir, gitURL, f.GitAuth())
	} else {
		a.repo, err = gitrepo.Temp(gitURL, f.GitAuth())
	}
	if err != nil {
		return err
	}
	a.repo.Logf = a.logf
	a.logf("fetching %s from %s", o.target, redactURL(gitURL))
	return a.repo.Fetch(ctx, o.target)
}

// rebaseURL swaps scheme and host of u for those of base, keeping the path.
func rebaseURL(u, base string) string {
	pu, err1 := url.Parse(u)
	pb, err2 := url.Parse(base)
	if err1 != nil || err2 != nil || pb.Host == "" {
		return u
	}
	path := pu.Path
	if bp := strings.TrimRight(pb.Path, "/"); bp != "" && !strings.HasPrefix(path, bp+"/") {
		path = bp + path
	}
	pu.Scheme, pu.Host, pu.Path = pb.Scheme, pb.Host, path
	return pu.String()
}

func redactURL(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.User == nil {
		return u
	}
	p.User = nil
	return p.String()
}

func detectForge(ctx context.Context, o *options) (string, error) {
	if o.forge != "" {
		k := strings.ToLower(o.forge)
		if k == "forgejo" || k == "codeberg" {
			k = "gitea"
		}
		return k, nil
	}
	switch {
	case os.Getenv("GITEA_ACTIONS") == "true" || os.Getenv("FORGEJO_ACTIONS") == "true":
		return "gitea", nil
	case os.Getenv("GITLAB_CI") == "true":
		return "gitlab", nil
	case os.Getenv("GITHUB_ACTIONS") == "true":
		return "github", nil
	}
	if o.serverURL == "" {
		return "", errors.New("cannot detect the forge: pass --forge and --server-url")
	}
	u, err := url.Parse(o.serverURL)
	if err != nil {
		return "", fmt.Errorf("--server-url: %w", err)
	}
	switch u.Host {
	case "github.com":
		return "github", nil
	case "gitlab.com":
		return "gitlab", nil
	case "codeberg.org":
		return "gitea", nil
	}
	// Probe the self-hosted server.
	client := &http.Client{Timeout: 10 * time.Second}
	probe := func(path string) bool {
		req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(o.serverURL, "/")+path, nil)
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	}
	switch {
	case probe("/api/v1/version"):
		return "gitea", nil
	case probe("/api/v4/version") || probe("/-/health"):
		return "gitlab", nil
	case probe("/api/v3/meta"):
		return "github", nil
	}
	return "", fmt.Errorf("cannot detect the forge at %s: pass --forge", o.serverURL)
}

// runner loads configuration from the target branch and builds a Runner.
func (a *app) runner(ctx context.Context) (*manifest.Runner, error) {
	o := a.o
	ref := gitrepo.BranchRef(o.target)
	if err := a.repo.Fetch(ctx, o.target); err != nil {
		return nil, err
	}
	read := func(p string) (string, bool, error) { return a.repo.ReadFile(ctx, ref, p) }

	cfgNames := config.ConfigFiles
	if o.configFile != "" {
		cfgNames = []string{o.configFile}
	}
	var cfg *config.Manifest
	var cfgName string
	for _, n := range cfgNames {
		c, ok, err := read(n)
		if err != nil {
			return nil, err
		}
		if ok {
			if cfg, err = config.Parse([]byte(c)); err != nil {
				return nil, fmt.Errorf("%s: %w", n, err)
			}
			cfgName = n
			break
		}
	}
	r := &manifest.Runner{
		Forge: a.forge, Repo: a.repo, Target: o.target, DryRun: o.dryRun,
		Author: gitrepo.Identity{Name: o.userName, Email: o.userEmail},
		Logf:   a.logf,
		Out:    func(f string, args ...any) { fmt.Fprintf(a.stdout, f, args...) },
	}
	switch {
	case cfg != nil:
		a.debugf("using config %s", cfgName)
		r.Config = cfg
		manNames := config.ManifestFiles
		if o.manifestFile != "" {
			manNames = []string{o.manifestFile}
		} else if strings.HasPrefix(cfgName, "release-please") {
			manNames = []string{".release-please-manifest.json", ".publish-please-manifest.json"}
		}
		r.ManifestFile = manNames[0]
		for _, n := range manNames {
			c, ok, err := read(n)
			if err != nil {
				return nil, err
			}
			if ok {
				if r.Versions, err = config.ParseVersions([]byte(c)); err != nil {
					return nil, fmt.Errorf("%s: %w", n, err)
				}
				r.ManifestFile = n
				break
			}
		}
	case o.releaseType != "":
		r.Config = config.SingleWith(config.Package{
			Path: config.NormalisePath(o.path), ReleaseType: o.releaseType,
			PackageName: o.packageName, Component: o.component,
		})
	default:
		return nil, fmt.Errorf("no %s on %s; add one (see `publish-please bootstrap`) or pass --release-type", strings.Join(cfgNames, " or "), o.target)
	}
	for _, p := range r.Config.Packages {
		if _, err := strategy.Get(p.ReleaseType); err != nil {
			return nil, err
		}
	}
	if err := r.Init(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// report prints a summary and writes CI outputs.
func (a *app) report(rels []manifest.CreatedRelease, prs []manifest.PRResult) error {
	out := outputs(rels, prs)
	if a.o.outputsFile != "" && !a.o.dryRun {
		f, err := os.OpenFile(a.o.outputsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("outputs file: %w", err)
		}
		keys := make([]string, 0, len(out))
		for k := range out {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(f, "%s=%s\n", k, out[k])
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	if a.o.jsonOut {
		enc := json.NewEncoder(a.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	return nil
}

func outputs(rels []manifest.CreatedRelease, prs []manifest.PRResult) map[string]string {
	out := map[string]string{
		"releases_created": "false",
		"release_created":  "false",
		"prs_created":      "false",
		"paths_released":   "[]",
		"prs":              "[]",
	}
	var paths []string
	for _, r := range rels {
		paths = append(paths, r.Path)
		out["releases_created"] = "true"
		prefix := ""
		if r.Path != config.RootPath {
			prefix = r.Path + "--"
		} else {
			out["release_created"] = "true"
		}
		set := func(k, v string) { out[prefix+k] = v }
		set("release_created", "true")
		set("tag_name", r.Tag)
		set("version", r.Version.String())
		set("major", fmt.Sprint(r.Version.Major))
		set("minor", fmt.Sprint(r.Version.Minor))
		set("patch", fmt.Sprint(r.Version.Patch))
		set("sha", r.SHA)
		set("html_url", r.URL)
		set("upload_url", r.UploadURL)
		set("pr", fmt.Sprint(r.PR))
	}
	if len(paths) > 0 {
		b, _ := json.Marshal(paths)
		out["paths_released"] = string(b)
	}
	type prOut struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		URL     string `json:"url"`
		Branch  string `json:"headBranchName"`
		Created bool   `json:"created"`
	}
	var list []prOut
	for _, p := range prs {
		if p.PR == nil {
			continue
		}
		list = append(list, prOut{Number: p.PR.Number, Title: p.PR.Title, URL: p.PR.URL, Branch: p.PR.HeadBranch, Created: p.Created})
	}
	if len(list) > 0 {
		out["prs_created"] = "true"
		b, _ := json.Marshal(list[0])
		out["pr"] = string(b)
		b, _ = json.Marshal(list)
		out["prs"] = string(b)
	}
	return out
}
