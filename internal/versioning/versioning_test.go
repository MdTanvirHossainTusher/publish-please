package versioning

import (
	"testing"

	"github.com/MdTanvirHossainTusher/publish-please/internal/commits"
	"github.com/MdTanvirHossainTusher/publish-please/internal/semver"
)

func v(s string) *semver.Version { x := semver.MustParse(s); return &x }

var (
	fix      = commits.Change{Type: "fix"}
	feat     = commits.Change{Type: "feat"}
	breaking = commits.Change{Type: "fix", Breaking: true}
)

func TestNext(t *testing.T) {
	cases := []struct {
		name    string
		cur     *semver.Version
		changes []commits.Change
		opts    Options
		want    string
	}{
		{"initial", nil, []commits.Change{feat}, Options{}, "1.0.0"},
		{"initial custom", nil, []commits.Change{feat}, Options{InitialVersion: "0.1.0"}, "0.1.0"},
		{"patch", v("1.2.3"), []commits.Change{fix}, Options{}, "1.2.4"},
		{"minor", v("1.2.3"), []commits.Change{fix, feat}, Options{}, "1.3.0"},
		{"major", v("1.2.3"), []commits.Change{feat, breaking}, Options{}, "2.0.0"},
		{"pre-major breaking", v("0.4.1"), []commits.Change{breaking}, Options{BumpMinorPreMajor: true}, "0.5.0"},
		{"pre-major feat", v("0.4.1"), []commits.Change{feat}, Options{BumpPatchForMinorPreMajor: true}, "0.4.2"},
		{"pre-major plain", v("0.4.1"), []commits.Change{breaking}, Options{}, "1.0.0"},
		{"always patch", v("1.2.3"), []commits.Change{breaking}, Options{Versioning: AlwaysBumpPatch}, "1.2.4"},
		{"always minor", v("1.2.3"), []commits.Change{fix}, Options{Versioning: AlwaysBumpMinor}, "1.3.0"},
		{"always major", v("1.2.3"), []commits.Change{fix}, Options{Versioning: AlwaysBumpMajor}, "2.0.0"},
		{"release-as config", v("1.2.3"), []commits.Change{fix}, Options{ReleaseAs: "3.0.0"}, "3.0.0"},
		{"release-as footer", v("1.2.3"), []commits.Change{{Type: "chore", ReleaseAs: "1.5.0"}}, Options{}, "1.5.0"},
		{"prerelease start", v("1.2.3"), []commits.Change{feat}, Options{Prerelease: true}, "1.3.0-beta.1"},
		{"prerelease next", v("1.3.0-beta.1"), []commits.Change{feat}, Options{Prerelease: true}, "1.3.0-beta.2"},
		{"prerelease escalate", v("1.3.0-beta.1"), []commits.Change{breaking}, Options{Versioning: Prerelease, PrereleaseType: "rc"}, "2.0.0-rc.1"},
		{"prerelease custom counter", v("1.3.0-alpha01"), []commits.Change{fix}, Options{Versioning: Prerelease}, "1.3.0-alpha02"},
		{"graduate", v("1.3.0-beta.4"), []commits.Change{feat}, Options{}, "1.3.0"},
		{"graduate escalate", v("1.3.0-beta.4"), []commits.Change{breaking}, Options{}, "2.0.0"},
		{"service pack", v("1.2.3"), []commits.Change{fix}, Options{Versioning: ServicePack}, "1.2.3-sp.1"},
		{"service pack 2", v("1.2.3-sp.1"), []commits.Change{fix}, Options{Versioning: ServicePack}, "1.2.3-sp.2"},
	}
	for _, c := range cases {
		got, err := Next(c.cur, c.changes, c.opts)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.String() != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestSemverCompare(t *testing.T) {
	order := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0"}
	for i := 0; i+1 < len(order); i++ {
		if semver.Compare(semver.MustParse(order[i]), semver.MustParse(order[i+1])) != -1 {
			t.Errorf("%s should be < %s", order[i], order[i+1])
		}
	}
}
