package updaters

import (
	"strings"
	"testing"
)

func run(t *testing.T, u Updater, in string) string {
	t.Helper()
	out, err := u.Update(in, true)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestJSONPreservesFormatting(t *testing.T) {
	in := "{\n    \"name\": \"x\",\n    \"nested\": {\"a\": [1, {\"b\": \"}\"}]},\n    \"version\":   \"1.0.0\", \"z\": true\n}\n"
	out := run(t, JSONField("$.version", "1.1.0"), in)
	want := strings.Replace(in, `"1.0.0"`, `"1.1.0"`, 1)
	if out != want {
		t.Fatalf("got\n%s", out)
	}
}

func TestJSONPackageLockRootKey(t *testing.T) {
	in := `{"version":"1.0.0","packages":{"":{"version":"1.0.0"},"node_modules/a":{"version":"1.0.0"}}}`
	out := run(t, JSONField(`$.packages[''].version`, "2.0.0"), in)
	if out != `{"version":"1.0.0","packages":{"":{"version":"2.0.0"},"node_modules/a":{"version":"1.0.0"}}}` {
		t.Fatalf("got %s", out)
	}
	out = run(t, JSONField(`$.packages["node_modules/a"].version`, "3.0.0"), in)
	if !strings.Contains(out, `"node_modules/a":{"version":"3.0.0"}`) {
		t.Fatalf("got %s", out)
	}
}

func TestJSONArrayIndex(t *testing.T) {
	out := run(t, JSONField("$.items[1].v", "9.9.9"), `{"items":[{"v":"1"},{"v":"2"}]}`)
	if out != `{"items":[{"v":"1"},{"v":"9.9.9"}]}` {
		t.Fatalf("got %s", out)
	}
}

func TestJSONMissing(t *testing.T) {
	if _, err := JSONField("$.nope", "1").Update(`{"a":1}`, true); err == nil {
		t.Fatal("expected error")
	}
}

func TestYAML(t *testing.T) {
	in := "# chart\napiVersion: v2\nname: demo # the name\nversion: 0.1.0 # bump me\nappVersion: \"1.16.0\"\ndeps:\n  - name: a\n    version: '2.0.0'\n"
	out := run(t, YAMLField("$.version", "0.2.0"), in)
	out = run(t, YAMLField("$.appVersion", "1.17.0"), out)
	out = run(t, YAMLField("$.deps[0].version", "2.1.0"), out)
	want := "# chart\napiVersion: v2\nname: demo # the name\nversion: 0.2.0 # bump me\nappVersion: \"1.17.0\"\ndeps:\n  - name: a\n    version: '2.1.0'\n"
	if out != want {
		t.Fatalf("got\n%s", out)
	}
}

func TestTOML(t *testing.T) {
	in := "[package]\nname = \"demo\"\nversion = \"0.1.0\" # x\n\n[dependencies]\nversion = \"9\"\n\n[tool.poetry]\nversion = '1.0.0'\n"
	out := run(t, TOMLField("$.package.version", "0.2.0"), in)
	out = run(t, TOMLField("$.tool.poetry.version", "1.1.0"), out)
	want := "[package]\nname = \"demo\"\nversion = \"0.2.0\" # x\n\n[dependencies]\nversion = \"9\"\n\n[tool.poetry]\nversion = '1.1.0'\n"
	if out != want {
		t.Fatalf("got\n%s", out)
	}
}

func TestCargoLock(t *testing.T) {
	in := "[[package]]\nname = \"a\"\nversion = \"0.1.0\"\n\n[[package]]\nname = \"b\"\nversion = \"0.1.0\"\n"
	out := run(t, CargoLock(map[string]string{"b": "0.2.0"}), in)
	if out != "[[package]]\nname = \"a\"\nversion = \"0.1.0\"\n\n[[package]]\nname = \"b\"\nversion = \"0.2.0\"\n" {
		t.Fatalf("got\n%s", out)
	}
}

func TestXMLPom(t *testing.T) {
	in := `<?xml version="1.0"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <parent><groupId>g</groupId><version>9.9.9</version></parent>
  <artifactId>demo</artifactId>
  <version>1.0.0-SNAPSHOT</version>
  <dependencies><dependency><version>3.0</version></dependency></dependencies>
</project>
`
	out := run(t, XMLField("/project/version", "1.1.0"), in)
	if !strings.Contains(out, "<version>1.1.0</version>\n  <dependencies>") || !strings.Contains(out, "<version>9.9.9</version></parent>") {
		t.Fatalf("got\n%s", out)
	}
}

func TestGenericMarkers(t *testing.T) {
	in := `const VERSION = "1.0.0"; // x-release-please-version
const MAJOR = 1; // x-release-please-major
<!-- x-release-please-start-version -->
install foo@1.0.0 and foo@1.0.0
<!-- x-release-please-end -->
untouched 1.0.0
# x-publish-please-version
v = "1.0.0" # x-publish-please-version
`
	out := run(t, Generic(2, 1, 0, "2.1.0"), in)
	want := `const VERSION = "2.1.0"; // x-release-please-version
const MAJOR = 2; // x-release-please-major
<!-- x-release-please-start-version -->
install foo@2.1.0 and foo@2.1.0
<!-- x-release-please-end -->
untouched 1.0.0
# x-publish-please-version
v = "2.1.0" # x-publish-please-version
`
	if out != want {
		t.Fatalf("got\n%s", out)
	}
}

func TestRegex(t *testing.T) {
	out := run(t, Regex(`(?m)^version=(.+)$`, "1.2.0"), "group=x\nversion=1.1.0\n")
	if out != "group=x\nversion=1.2.0\n" {
		t.Fatalf("got %q", out)
	}
}
