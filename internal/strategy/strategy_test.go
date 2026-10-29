package strategy

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/MdTanvirHossainTusher/publish-please/internal/config"
	"github.com/MdTanvirHossainTusher/publish-please/internal/semver"
)

type memFiles map[string]string

func (m memFiles) Read(_ context.Context, p string) (string, bool, error) {
	c, ok := m[p]
	return c, ok, nil
}

func (m memFiles) List(_ context.Context, dir string) ([]string, error) {
	var out []string
	for p := range m {
		if dir == "." || dir == "" || strings.HasPrefix(p, dir+"/") {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

// apply runs a strategy and returns the changed files.
func apply(t *testing.T, typ string, pkg config.Package, files memFiles, version string) map[string]string {
	t.Helper()
	if pkg.Path == "" {
		pkg.Path = "."
	}
	pkg.ReleaseType = typ
	s, err := Get(typ)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	v := semver.MustParse(version)
	us, err := s.Updates(ctx, &pkg, v, files)
	if err != nil {
		t.Fatal(err)
	}
	extra, err := ExtraFileUpdates(ctx, &pkg, v, files)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, u := range append(us, extra...) {
		c, ok := out[u.Path]
		if !ok {
			c, ok = files[u.Path]
		}
		if !ok && !u.Create {
			continue
		}
		n, err := u.Updater.Update(c, ok)
		if err != nil {
			if u.Optional {
				continue
			}
			t.Fatalf("%s: %v", u.Path, err)
		}
		if n != c {
			out[u.Path] = n
		}
	}
	return out
}

func has(t *testing.T, files map[string]string, path string, subs ...string) {
	t.Helper()
	c, ok := files[path]
	if !ok {
		t.Fatalf("%s was not updated (updated: %v)", path, keys(files))
	}
	for _, s := range subs {
		if !strings.Contains(c, s) {
			t.Fatalf("%s missing %q:\n%s", path, s, c)
		}
	}
}

func keys(m map[string]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func TestRust(t *testing.T) {
	out := apply(t, "rust", config.Package{Path: "crates/foo"}, memFiles{
		"crates/foo/Cargo.toml": "[package]\nname = \"foo\"\nversion = \"0.1.0\"\n\n[dependencies]\nserde = { version = \"1\" }\n",
		"Cargo.lock":            "[[package]]\nname = \"foo\"\nversion = \"0.1.0\"\n\n[[package]]\nname = \"serde\"\nversion = \"1.0.0\"\n",
	}, "0.2.0")
	has(t, out, "crates/foo/Cargo.toml", `version = "0.2.0"`, `serde = { version = "1" }`)
	has(t, out, "Cargo.lock", "name = \"foo\"\nversion = \"0.2.0\"", "name = \"serde\"\nversion = \"1.0.0\"")
}

func TestPython(t *testing.T) {
	out := apply(t, "python", config.Package{}, memFiles{
		"pyproject.toml":         "[project]\nname = \"my-lib\"\nversion = \"1.0.0\"\n",
		"src/my_lib/__init__.py": "__version__ = \"1.0.0\"\n",
		"setup.cfg":              "[metadata]\nname = my-lib\nversion = 1.0.0\n",
	}, "1.1.0")
	has(t, out, "pyproject.toml", `version = "1.1.0"`)
	has(t, out, "src/my_lib/__init__.py", `__version__ = "1.1.0"`)
	has(t, out, "setup.cfg", "version = 1.1.0")
}

func TestMavenAndGradle(t *testing.T) {
	out := apply(t, "maven", config.Package{}, memFiles{
		"pom.xml": "<project>\n  <parent><version>3.2.0</version></parent>\n  <artifactId>svc</artifactId>\n  <version>1.0.0</version>\n</project>\n",
	}, "1.1.0")
	has(t, out, "pom.xml", "<version>1.1.0</version>\n</project>", "<parent><version>3.2.0</version>")

	out = apply(t, "gradle", config.Package{}, memFiles{
		"gradle.properties": "group=com.x\nversion=1.0.0\n",
		"build.gradle.kts":  "plugins { java }\nversion = \"1.0.0\"\n",
	}, "1.0.1")
	has(t, out, "gradle.properties", "version=1.0.1")
	has(t, out, "build.gradle.kts", `version = "1.0.1"`)
}

func TestHelmDartElixirRuby(t *testing.T) {
	out := apply(t, "helm", config.Package{}, memFiles{"Chart.yaml": "apiVersion: v2\nname: web\nversion: 0.1.0\nappVersion: \"1.0\"\n"}, "0.2.0")
	has(t, out, "Chart.yaml", "version: 0.2.0\nappVersion: \"1.0\"")

	out = apply(t, "dart", config.Package{}, memFiles{"pubspec.yaml": "name: app\nversion: 1.2.3+7\n"}, "1.3.0")
	has(t, out, "pubspec.yaml", "version: 1.3.0+8")

	out = apply(t, "elixir", config.Package{}, memFiles{"mix.exs": "def project do\n  [app: :x, version: \"0.1.0\"]\nend\n"}, "0.2.0")
	has(t, out, "mix.exs", `version: "0.2.0"`)

	out = apply(t, "ruby", config.Package{}, memFiles{"lib/my_gem/version.rb": "module MyGem\n  VERSION = \"1.0.0\"\nend\n"}, "1.0.1")
	has(t, out, "lib/my_gem/version.rb", `VERSION = "1.0.1"`)
}

func TestSimpleAndExtraFiles(t *testing.T) {
	pkg := config.Package{ExtraFiles: []config.ExtraFile{
		{Type: "json", Path: "app.json", JSONPath: "$.expo.version"},
		{Type: "yaml", Path: "/deploy/values.yaml", JSONPath: "$.image.tag"},
		{Type: "generic", Path: "docs/**/*.md", Glob: true},
	}}
	out := apply(t, "simple", pkg, memFiles{
		"app.json":            "{\"expo\": {\"version\": \"1.0.0\"}}",
		"deploy/values.yaml":  "image:\n  repo: x\n  tag: \"1.0.0\"\n",
		"docs/guide/intro.md": "Install v1.0.0 <!-- x-release-please-version -->\n",
		"docs/other.txt":      "1.0.0 x-release-please-version\n",
	}, "1.2.0")
	has(t, out, "version.txt", "1.2.0")
	has(t, out, "app.json", `"version": "1.2.0"`)
	has(t, out, "deploy/values.yaml", `tag: "1.2.0"`)
	has(t, out, "docs/guide/intro.md", "Install v1.2.0")
	if _, ok := out["docs/other.txt"]; ok {
		t.Fatal("glob matched a non-.md file")
	}
}

func TestNodeName(t *testing.T) {
	f := memFiles{"pkgs/a/package.json": `{"name":"@scope/a","version":"1.0.0"}`}
	s, _ := Get("node")
	n := s.DetectName(context.Background(), &config.Package{Path: "pkgs/a"}, f)
	if n != "@scope/a" || ShortName(n) != "a" {
		t.Fatalf("got %q", n)
	}
}
