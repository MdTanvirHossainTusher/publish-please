// Package strategy knows which files carry the version for each release type.
package strategy

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/MdTanvirHossainTusher/publish-please/internal/config"
	"github.com/MdTanvirHossainTusher/publish-please/internal/semver"
	"github.com/MdTanvirHossainTusher/publish-please/internal/updaters"
)

// Files gives read access to the repository at the release base commit.
type Files interface {
	Read(ctx context.Context, path string) (string, bool, error)
	List(ctx context.Context, dir string) ([]string, error)
}

// Strategy builds the version file updates for one release type.
type Strategy interface {
	// DetectName returns the package name declared in the package's manifest
	// file (package.json, Cargo.toml, ...), or "".
	DetectName(ctx context.Context, pkg *config.Package, f Files) string
	// Updates returns the files to change (changelog excluded).
	Updates(ctx context.Context, pkg *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error)
}

type spec struct {
	name    func(ctx context.Context, pkg *config.Package, f Files) string
	updates func(ctx context.Context, pkg *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error)
}

func (s spec) DetectName(ctx context.Context, p *config.Package, f Files) string {
	if s.name == nil {
		return ""
	}
	return s.name(ctx, p, f)
}

func (s spec) Updates(ctx context.Context, p *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error) {
	if s.updates == nil {
		return nil, nil
	}
	return s.updates(ctx, p, v, f)
}

var registry = map[string]Strategy{}

func init() {
	registry["simple"] = spec{updates: simpleUpdates}
	registry["node"] = spec{name: nodeName, updates: nodeUpdates}
	registry["go"] = spec{updates: versionFileOnly}
	registry["python"] = spec{name: pythonName, updates: pythonUpdates}
	registry["rust"] = spec{name: rustName, updates: rustUpdates}
	registry["maven"] = spec{name: mavenName, updates: mavenUpdates}
	registry["java"] = registry["maven"]
	registry["gradle"] = spec{updates: gradleUpdates}
	registry["helm"] = spec{name: yamlName("Chart.yaml"), updates: helmUpdates}
	registry["php"] = spec{name: jsonName("composer.json"), updates: phpUpdates}
	registry["ruby"] = spec{updates: rubyUpdates}
	registry["dart"] = spec{name: yamlName("pubspec.yaml"), updates: dartUpdates}
	registry["elixir"] = spec{updates: elixirUpdates}
	registry["terraform-module"] = spec{updates: terraformUpdates}
	registry["dotnet"] = spec{updates: dotnetUpdates}
	registry["bazel"] = spec{updates: bazelUpdates}
	registry["expo"] = registry["node"]
	registry["ocaml"] = spec{updates: ocamlUpdates}
}

// Get returns the strategy for a release type.
func Get(releaseType string) (Strategy, error) {
	s, ok := registry[releaseType]
	if !ok {
		return nil, fmt.Errorf("unsupported release-type %q (supported: %s)", releaseType, strings.Join(Types(), ", "))
	}
	return s, nil
}

// Types lists supported release types.
func Types() []string {
	ts := make([]string, 0, len(registry))
	for t := range registry {
		ts = append(ts, t)
	}
	sort.Strings(ts)
	return ts
}

func at(p *config.Package, file string) string { return config.Join(p.Path, file) }

func read(ctx context.Context, f Files, p string) string {
	c, _, _ := f.Read(ctx, p)
	return c
}

// ExtraFileUpdates builds updates for the "extra-files" option.
func ExtraFileUpdates(ctx context.Context, p *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error) {
	var out []updaters.FileUpdate
	for _, ef := range p.ExtraFiles {
		paths := []string{at(p, ef.Path)}
		if ef.Glob {
			matched, err := globFiles(ctx, p, ef.Path, f)
			if err != nil {
				return nil, err
			}
			paths = matched
		}
		for _, fp := range paths {
			u, err := extraUpdater(ef, v)
			if err != nil {
				return nil, err
			}
			out = append(out, updaters.FileUpdate{Path: fp, Updater: u})
		}
	}
	return out, nil
}

func extraUpdater(ef config.ExtraFile, v semver.Version) (updaters.Updater, error) {
	vs := v.String()
	switch ef.Type {
	case "generic", "":
		return updaters.Generic(v.Major, v.Minor, v.Patch, vs), nil
	case "json":
		return updaters.JSONField(ef.JSONPath, vs), nil
	case "yaml":
		return updaters.YAMLField(ef.JSONPath, vs), nil
	case "toml":
		return updaters.TOMLField(ef.JSONPath, vs), nil
	case "xml", "pom":
		xp := ef.XPath
		if ef.Type == "pom" && xp == "" {
			xp = "/project/version"
		}
		return updaters.XMLField(xp, vs), nil
	}
	return nil, fmt.Errorf("extra-files: unknown type %q for %s", ef.Type, ef.Path)
}

func globFiles(ctx context.Context, p *config.Package, pattern string, f Files) ([]string, error) {
	base := p.Path
	if strings.HasPrefix(pattern, "/") {
		base = config.RootPath
		pattern = strings.TrimPrefix(pattern, "/")
	}
	all, err := f.List(ctx, base)
	if err != nil {
		return nil, err
	}
	re := globToRegexp(pattern)
	var out []string
	for _, fp := range all {
		rel := fp
		if base != config.RootPath {
			rel = strings.TrimPrefix(fp, base+"/")
		}
		if re.MatchString(rel) {
			out = append(out, fp)
		}
	}
	return out, nil
}

// globToRegexp supports *, ** and ?.
func globToRegexp(g string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*':
			if i+1 < len(g) && g[i+1] == '*' {
				i++
				if i+1 < len(g) && g[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// ---- simple / go ----

func simpleUpdates(ctx context.Context, p *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error) {
	file := p.VersionFile
	if file == "" {
		file = "version.txt"
	}
	return []updaters.FileUpdate{{Path: at(p, file), Create: true, Updater: updaters.VersionTxt(v.String())}}, nil
}

var versionAssignRe = `(?m)(?:VERSION|__version__|[Vv]ersion)\s*(?::=|=|:)\s*["']([^"']+)["']`

func versionFileOnly(ctx context.Context, p *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error) {
	if p.VersionFile == "" {
		return nil, nil
	}
	return []updaters.FileUpdate{versionFileUpdate(p, v)}, nil
}

// versionFileUpdate updates markers if present, otherwise the first
// VERSION = "x" style assignment.
func versionFileUpdate(p *config.Package, v semver.Version) updaters.FileUpdate {
	gen := updaters.Generic(v.Major, v.Minor, v.Patch, v.String())
	re := updaters.Regex(versionAssignRe, v.String())
	return updaters.FileUpdate{Path: at(p, p.VersionFile), Updater: updaters.Func(func(c string, e bool) (string, error) {
		out, err := gen.Update(c, e)
		if err != nil || out != c {
			return out, err
		}
		return re.Update(c, e)
	})}
}

// ---- node ----

func nodeName(ctx context.Context, p *config.Package, f Files) string {
	c, ok, _ := f.Read(ctx, at(p, "package.json"))
	if !ok {
		return ""
	}
	var pj struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal([]byte(c), &pj)
	return pj.Name
}

func nodeUpdates(ctx context.Context, p *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error) {
	vs := v.String()
	out := []updaters.FileUpdate{{Path: at(p, "package.json"), Updater: updaters.JSONField("$.version", vs)}}
	for _, lock := range []string{"package-lock.json", "npm-shrinkwrap.json"} {
		out = append(out,
			updaters.FileUpdate{Path: at(p, lock), Optional: true, Updater: updaters.JSONField("$.version", vs)},
			updaters.FileUpdate{Path: at(p, lock), Optional: true, Updater: updaters.JSONField(`$.packages[""].version`, vs)},
		)
	}
	return out, nil
}

// ---- python ----

func pythonName(ctx context.Context, p *config.Package, f Files) string {
	c := read(ctx, f, at(p, "pyproject.toml"))
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?ms)^\[project\][^\[]*?^name\s*=\s*["']([^"']+)["']`),
		regexp.MustCompile(`(?ms)^\[tool\.poetry\][^\[]*?^name\s*=\s*["']([^"']+)["']`),
	} {
		if m := re.FindStringSubmatch(c); m != nil {
			return m[1]
		}
	}
	if m := regexp.MustCompile(`(?ms)^\[metadata\][^\[]*?^name\s*=\s*(\S+)`).FindStringSubmatch(read(ctx, f, at(p, "setup.cfg"))); m != nil {
		return m[1]
	}
	return ""
}

func pythonUpdates(ctx context.Context, p *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error) {
	vs := v.String()
	out := []updaters.FileUpdate{
		{Path: at(p, "pyproject.toml"), Optional: true, Updater: updaters.TOMLField("$.project.version", vs)},
		{Path: at(p, "pyproject.toml"), Optional: true, Updater: updaters.TOMLField("$.tool.poetry.version", vs)},
		{Path: at(p, "setup.cfg"), Optional: true, Updater: updaters.Regex(`(?m)^version\s*=\s*(\S+)\s*$`, vs)},
		{Path: at(p, "setup.py"), Optional: true, Updater: updaters.Regex(`version\s*=\s*["']([^"']+)["']`, vs)},
	}
	if p.VersionFile != "" {
		out = append(out, versionFileUpdate(p, v))
	} else if name := pythonName(ctx, p, f); name != "" {
		mod := strings.ReplaceAll(name, "-", "_")
		for _, cand := range []string{mod + "/__init__.py", "src/" + mod + "/__init__.py", mod + "/version.py", "src/" + mod + "/version.py"} {
			out = append(out, updaters.FileUpdate{Path: at(p, cand), Optional: true, Updater: updaters.Regex(`__version__\s*=\s*["']([^"']+)["']`, vs)})
		}
	}
	return out, nil
}

// ---- rust ----

func rustName(ctx context.Context, p *config.Package, f Files) string {
	c := read(ctx, f, at(p, "Cargo.toml"))
	if m := regexp.MustCompile(`(?ms)^\[package\][^\[]*?^name\s*=\s*["']([^"']+)["']`).FindStringSubmatch(c); m != nil {
		return m[1]
	}
	return ""
}

func rustUpdates(ctx context.Context, p *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error) {
	vs := v.String()
	out := []updaters.FileUpdate{{Path: at(p, "Cargo.toml"), Updater: updaters.TOMLField("$.package.version", vs)}}
	if name := rustName(ctx, p, f); name != "" {
		lock := map[string]string{name: vs}
		// The lock file lives next to the crate or at the workspace root.
		for _, l := range []string{at(p, "Cargo.lock"), "Cargo.lock"} {
			out = append(out, updaters.FileUpdate{Path: l, Optional: true, Updater: updaters.CargoLock(lock)})
			if p.IsRoot() {
				break
			}
		}
	}
	return out, nil
}

// ---- maven / gradle ----

func mavenName(ctx context.Context, p *config.Package, f Files) string {
	c := read(ctx, f, at(p, "pom.xml"))
	// artifactId directly under <project>, after any <parent> block
	c = regexp.MustCompile(`(?s)<parent>.*?</parent>`).ReplaceAllString(c, "")
	if m := regexp.MustCompile(`<artifactId>\s*([^<\s]+)\s*</artifactId>`).FindStringSubmatch(c); m != nil {
		return m[1]
	}
	return ""
}

func mavenUpdates(ctx context.Context, p *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error) {
	return []updaters.FileUpdate{{Path: at(p, "pom.xml"), Updater: updaters.XMLField("/project/version", v.String())}}, nil
}

func gradleUpdates(ctx context.Context, p *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error) {
	vs := v.String()
	return []updaters.FileUpdate{
		{Path: at(p, "gradle.properties"), Optional: true, Updater: updaters.Regex(`(?m)^\s*version\s*=\s*(\S+)\s*$`, vs)},
		{Path: at(p, "build.gradle"), Optional: true, Updater: updaters.Regex(`(?m)^\s*version\s*=?\s*["']([^"']+)["']`, vs)},
		{Path: at(p, "build.gradle.kts"), Optional: true, Updater: updaters.Regex(`(?m)^\s*version\s*=\s*"([^"]+)"`, vs)},
	}, nil
}

// ---- yaml / json based ----

func yamlName(file string) func(ctx context.Context, p *config.Package, f Files) string {
	return func(ctx context.Context, p *config.Package, f Files) string {
		if m := regexp.MustCompile(`(?m)^name:\s*["']?([^"'\s#]+)`).FindStringSubmatch(read(ctx, f, at(p, file))); m != nil {
			return m[1]
		}
		return ""
	}
}

func jsonName(file string) func(ctx context.Context, p *config.Package, f Files) string {
	return func(ctx context.Context, p *config.Package, f Files) string {
		s, _ := updaters.GetJSONString(read(ctx, f, at(p, file)), updaters.Keys("name"))
		return s
	}
}

func helmUpdates(ctx context.Context, p *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error) {
	return []updaters.FileUpdate{{Path: at(p, "Chart.yaml"), Updater: updaters.YAMLField("$.version", v.String())}}, nil
}

func dartUpdates(ctx context.Context, p *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error) {
	vs := v.String()
	// Keep a Flutter build number: 1.2.3+4 -> 1.3.0+5
	u := updaters.Func(func(c string, e bool) (string, error) {
		cur := regexp.MustCompile(`(?m)^version:\s*["']?[^\s"'#]+?\+(\d+)`).FindStringSubmatch(c)
		next := vs
		if cur != nil && v.Build == "" {
			var n int
			fmt.Sscan(cur[1], &n)
			next = fmt.Sprintf("%s+%d", vs, n+1)
		}
		return updaters.YAMLField("$.version", next).Update(c, e)
	})
	return []updaters.FileUpdate{{Path: at(p, "pubspec.yaml"), Updater: u}}, nil
}

func phpUpdates(ctx context.Context, p *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error) {
	return []updaters.FileUpdate{{Path: at(p, "composer.json"), Optional: true, Updater: updaters.JSONField("$.version", v.String())}}, nil
}

// ---- ruby / elixir / terraform / dotnet / bazel / ocaml ----

func rubyUpdates(ctx context.Context, p *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error) {
	vs := v.String()
	re := `VERSION\s*=\s*["']([^"']+)["']`
	if p.VersionFile != "" {
		return []updaters.FileUpdate{{Path: at(p, p.VersionFile), Updater: updaters.Regex(re, vs)}}, nil
	}
	files, err := f.List(ctx, p.Path)
	if err != nil {
		return nil, err
	}
	var out []updaters.FileUpdate
	vre := globToRegexp("lib/**/version.rb")
	for _, fp := range files {
		rel := fp
		if !p.IsRoot() {
			rel = strings.TrimPrefix(fp, p.Path+"/")
		}
		if vre.MatchString(rel) {
			out = append(out, updaters.FileUpdate{Path: fp, Optional: true, Updater: updaters.Regex(re, vs)})
		}
	}
	return out, nil
}

func elixirUpdates(ctx context.Context, p *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error) {
	return []updaters.FileUpdate{{Path: at(p, "mix.exs"), Updater: updaters.Regex(`version:\s*"([^"]+)"`, v.String())}}, nil
}

func terraformUpdates(ctx context.Context, p *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error) {
	vs := v.String()
	return []updaters.FileUpdate{
		{Path: at(p, "README.md"), Optional: true, Updater: updaters.RegexAll(`version\s*=\s*"~>\s*(\d+\.\d+(?:\.\d+)?)"`, vs)},
		{Path: at(p, "versions.tf"), Optional: true, Updater: updaters.Generic(v.Major, v.Minor, v.Patch, vs)},
	}, nil
}

func dotnetUpdates(ctx context.Context, p *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error) {
	files, err := f.List(ctx, p.Path)
	if err != nil {
		return nil, err
	}
	var out []updaters.FileUpdate
	for _, fp := range files {
		if strings.HasSuffix(fp, ".csproj") || path.Base(fp) == "Directory.Build.props" {
			out = append(out, updaters.FileUpdate{Path: fp, Optional: true, Updater: updaters.XMLField("//PropertyGroup/Version", v.String())})
		}
	}
	return out, nil
}

func bazelUpdates(ctx context.Context, p *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error) {
	return []updaters.FileUpdate{{Path: at(p, "MODULE.bazel"), Updater: updaters.Regex(`(?s)module\([^)]*?version\s*=\s*"([^"]+)"`, v.String())}}, nil
}

func ocamlUpdates(ctx context.Context, p *config.Package, v semver.Version, f Files) ([]updaters.FileUpdate, error) {
	files, err := f.List(ctx, p.Path)
	if err != nil {
		return nil, err
	}
	var out []updaters.FileUpdate
	for _, fp := range files {
		switch {
		case strings.HasSuffix(fp, ".opam"):
			out = append(out, updaters.FileUpdate{Path: fp, Optional: true, Updater: updaters.Regex(`(?m)^version:\s*"([^"]+)"`, v.String())})
		case path.Base(fp) == "esy.json" || (path.Base(fp) == "package.json" && path.Dir(fp) == p.Path):
			out = append(out, updaters.FileUpdate{Path: fp, Optional: true, Updater: updaters.JSONField("$.version", v.String())})
		}
	}
	return out, nil
}

// ShortName strips an npm scope: "@org/pkg" -> "pkg".
func ShortName(n string) string {
	if i := strings.LastIndex(n, "/"); i >= 0 && strings.HasPrefix(n, "@") {
		return n[i+1:]
	}
	return n
}
