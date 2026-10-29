// Package config loads the release-please compatible manifest configuration.
//
// The format is the same as release-please's release-please-config.json and
// .release-please-manifest.json, so existing repositories work unchanged.
package config

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Default file names. The release-please names are tried after these.
var (
	ConfigFiles   = []string{"publish-please-config.json", "release-please-config.json"}
	ManifestFiles = []string{".publish-please-manifest.json", ".release-please-manifest.json"}
)

const (
	DefaultLabel        = "autorelease: pending"
	DefaultReleaseLabel = "autorelease: tagged"
	DefaultChangelog    = "CHANGELOG.md"
	DefaultTitlePattern = "chore${scope}: release${component} ${version}"
	DefaultGroupTitle   = "chore: release ${branch}"
	DefaultPRHeader     = ":robot: I have created a release *beep* *boop*"
	DefaultPRFooter     = "This PR was generated with [Publish Please](https://github.com/MdTanvirHossainTusher/publish-please)."
	RootPath            = "."
)

// Section maps a commit type to a changelog heading.
type Section struct {
	Type    string `json:"type"`
	Section string `json:"section"`
	Hidden  bool   `json:"hidden,omitempty"`
}

// DefaultSections matches release-please's defaults.
var DefaultSections = []Section{
	{Type: "feat", Section: "Features"},
	{Type: "fix", Section: "Bug Fixes"},
	{Type: "perf", Section: "Performance Improvements"},
	{Type: "revert", Section: "Reverts"},
	{Type: "deps", Section: "Dependencies"},
	{Type: "docs", Section: "Documentation", Hidden: true},
	{Type: "style", Section: "Styles", Hidden: true},
	{Type: "chore", Section: "Miscellaneous Chores", Hidden: true},
	{Type: "refactor", Section: "Code Refactoring", Hidden: true},
	{Type: "test", Section: "Tests", Hidden: true},
	{Type: "build", Section: "Build System", Hidden: true},
	{Type: "ci", Section: "Continuous Integration", Hidden: true},
}

// ExtraFile is an additional file whose version should be bumped.
type ExtraFile struct {
	Type     string `json:"type"` // generic, json, yaml, toml, xml
	Path     string `json:"path"`
	JSONPath string `json:"jsonpath,omitempty"`
	XPath    string `json:"xpath,omitempty"`
	Glob     bool   `json:"glob,omitempty"`
}

func (e *ExtraFile) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*e = ExtraFile{Type: "generic", Path: s}
		return nil
	}
	type alias ExtraFile
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	if a.Type == "" {
		a.Type = "generic"
	}
	*e = ExtraFile(a)
	return nil
}

// Plugin is a manifest plugin entry: either "name" or {"type": "name", ...}.
type Plugin struct {
	Type       string   `json:"type"`
	GroupName  string   `json:"groupName,omitempty"`
	Components []string `json:"components,omitempty"`
	Merge      *bool    `json:"merge,omitempty"`
	// UpdatePeerDependencies applies to node-workspace.
	UpdatePeerDependencies bool `json:"updatePeerDependencies,omitempty"`
}

func (p *Plugin) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*p = Plugin{Type: s}
		return nil
	}
	type alias Plugin
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*p = Plugin(a)
	return nil
}

// Package is the effective configuration of one releasable path.
type Package struct {
	Path string `json:"-"`

	ReleaseType               string      `json:"release-type"`
	PackageName               string      `json:"package-name"`
	Component                 string      `json:"component"`
	ChangelogPath             string      `json:"changelog-path"`
	ChangelogSections         []Section   `json:"changelog-sections"`
	ChangelogHost             string      `json:"changelog-host"`
	SkipChangelog             bool        `json:"skip-changelog"`
	DateFormat                string      `json:"date-format"`
	BumpMinorPreMajor         bool        `json:"bump-minor-pre-major"`
	BumpPatchForMinorPreMajor bool        `json:"bump-patch-for-minor-pre-major"`
	Versioning                string      `json:"versioning"`
	Prerelease                bool        `json:"prerelease"`
	PrereleaseType            string      `json:"prerelease-type"`
	ReleaseAs                 string      `json:"release-as"`
	InitialVersion            string      `json:"initial-version"`
	SkipGithubRelease         bool        `json:"skip-github-release"`
	SkipRelease               bool        `json:"skip-release"`
	Draft                     bool        `json:"draft"`
	DraftPullRequest          bool        `json:"draft-pull-request"`
	IncludeComponentInTag     *bool       `json:"include-component-in-tag"`
	IncludeVInTag             *bool       `json:"include-v-in-tag"`
	TagSeparator              string      `json:"tag-separator"`
	PullRequestTitlePattern   string      `json:"pull-request-title-pattern"`
	PullRequestHeader         string      `json:"pull-request-header"`
	PullRequestFooter         string      `json:"pull-request-footer"`
	ExcludePaths              []string    `json:"exclude-paths"`
	ExtraFiles                []ExtraFile `json:"extra-files"`
	VersionFile               string      `json:"version-file"`
}

// SkipsRelease reports whether creating the forge release is disabled.
func (p Package) SkipsRelease() bool { return p.SkipRelease || p.SkipGithubRelease }

// Sections returns the configured changelog sections or the defaults.
func (p Package) Sections() []Section {
	if len(p.ChangelogSections) > 0 {
		return p.ChangelogSections
	}
	return DefaultSections
}

// ChangelogFile returns the changelog path relative to the repo root.
func (p Package) ChangelogFile() string {
	cp := p.ChangelogPath
	if cp == "" {
		cp = DefaultChangelog
	}
	return Join(p.Path, cp)
}

// IsRoot reports whether the package lives at the repository root.
func (p Package) IsRoot() bool { return p.Path == RootPath || p.Path == "" }

// Join joins a package path and a file relative to it. A leading "/" in
// file makes it relative to the repository root instead.
func Join(pkgPath, file string) string {
	if strings.HasPrefix(file, "/") {
		return strings.TrimPrefix(file, "/")
	}
	if pkgPath == RootPath || pkgPath == "" {
		return path.Clean(file)
	}
	return path.Clean(path.Join(pkgPath, file))
}

// Manifest is the whole configuration file.
type Manifest struct {
	Packages map[string]*Package

	Plugins                      []Plugin
	SeparatePullRequests         bool
	BootstrapSHA                 string
	LastReleaseSHA               string
	GroupPullRequestTitlePattern string
	Labels                       []string
	ReleaseLabels                []string
	ExtraLabels                  []string
	ReleaseSearchDepth           int
	CommitSearchDepth            int
	AlwaysUpdate                 bool
	Signoff                      string
}

type rawManifest struct {
	Plugins                      []Plugin `json:"plugins"`
	SeparatePullRequests         bool     `json:"separate-pull-requests"`
	BootstrapSHA                 string   `json:"bootstrap-sha"`
	LastReleaseSHA               string   `json:"last-release-sha"`
	GroupPullRequestTitlePattern string   `json:"group-pull-request-title-pattern"`
	Label                        string   `json:"label"`
	ReleaseLabel                 string   `json:"release-label"`
	ExtraLabel                   string   `json:"extra-label"`
	ReleaseSearchDepth           int      `json:"release-search-depth"`
	CommitSearchDepth            int      `json:"commit-search-depth"`
	AlwaysUpdate                 bool     `json:"always-update"`
	Signoff                      string   `json:"signoff"`
}

// Parse parses a config file. Top-level package options are defaults that
// each entry under "packages" can override.
func Parse(data []byte) (*Manifest, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var rm rawManifest
	if err := json.Unmarshal(data, &rm); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	pkgsRaw := map[string]map[string]json.RawMessage{}
	if p, ok := top["packages"]; ok {
		if err := json.Unmarshal(p, &pkgsRaw); err != nil {
			return nil, fmt.Errorf("config: packages: %w", err)
		}
	}
	if len(pkgsRaw) == 0 {
		return nil, fmt.Errorf("config: no packages defined")
	}
	defaults := map[string]json.RawMessage{}
	for k, v := range top {
		switch k {
		case "packages", "plugins", "$schema":
			continue
		}
		defaults[k] = v
	}
	m := &Manifest{
		Packages:                     map[string]*Package{},
		Plugins:                      rm.Plugins,
		SeparatePullRequests:         rm.SeparatePullRequests,
		BootstrapSHA:                 rm.BootstrapSHA,
		LastReleaseSHA:               rm.LastReleaseSHA,
		GroupPullRequestTitlePattern: rm.GroupPullRequestTitlePattern,
		Labels:                       splitLabels(rm.Label, DefaultLabel),
		ReleaseLabels:                splitLabels(rm.ReleaseLabel, DefaultReleaseLabel),
		ExtraLabels:                  splitLabels(rm.ExtraLabel, ""),
		ReleaseSearchDepth:           rm.ReleaseSearchDepth,
		CommitSearchDepth:            rm.CommitSearchDepth,
		AlwaysUpdate:                 rm.AlwaysUpdate,
		Signoff:                      rm.Signoff,
	}
	if m.ReleaseSearchDepth <= 0 {
		m.ReleaseSearchDepth = 400
	}
	if m.CommitSearchDepth <= 0 {
		m.CommitSearchDepth = 500
	}
	for p, raw := range pkgsRaw {
		merged := map[string]json.RawMessage{}
		for k, v := range defaults {
			merged[k] = v
		}
		for k, v := range raw {
			merged[k] = v
		}
		b, _ := json.Marshal(merged)
		var pkg Package
		if err := json.Unmarshal(b, &pkg); err != nil {
			return nil, fmt.Errorf("config: package %q: %w", p, err)
		}
		pkg.Path = NormalisePath(p)
		if pkg.ReleaseType == "" {
			pkg.ReleaseType = "simple"
		}
		m.Packages[pkg.Path] = &pkg
	}
	return m, nil
}

// NormalisePath turns "./a/b/" into "a/b" and "" into ".".
func NormalisePath(p string) string {
	p = strings.Trim(strings.TrimPrefix(p, "./"), "/")
	if p == "" || p == "." {
		return RootPath
	}
	return path.Clean(p)
}

func splitLabels(s, def string) []string {
	if strings.TrimSpace(s) == "" {
		if def == "" {
			return nil
		}
		s = def
	}
	var out []string
	for _, l := range strings.Split(s, ",") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Paths returns package paths in a stable order (root first).
func (m *Manifest) Paths() []string {
	ps := make([]string, 0, len(m.Packages))
	for p := range m.Packages {
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool {
		if ps[i] == RootPath {
			return true
		}
		if ps[j] == RootPath {
			return false
		}
		return ps[i] < ps[j]
	})
	return ps
}

// ParseVersions parses the versions manifest (path -> version).
func ParseVersions(data []byte) (map[string]string, error) {
	raw := map[string]string{}
	if len(strings.TrimSpace(string(data))) == 0 {
		return raw, nil
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		out[NormalisePath(k)] = v
	}
	return out, nil
}

// SingleWith builds a one-package manifest from command-line options, for
// repos that have no config file.
func SingleWith(pkg Package) *Manifest {
	if pkg.Path == "" {
		pkg.Path = RootPath
	}
	if pkg.ReleaseType == "" {
		pkg.ReleaseType = "simple"
	}
	return &Manifest{
		Packages:           map[string]*Package{pkg.Path: &pkg},
		Labels:             []string{DefaultLabel},
		ReleaseLabels:      []string{DefaultReleaseLabel},
		ReleaseSearchDepth: 400,
		CommitSearchDepth:  500,
	}
}
