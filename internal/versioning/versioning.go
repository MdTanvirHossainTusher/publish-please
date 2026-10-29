// Package versioning decides the next version from a set of changes.
package versioning

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/MdTanvirHossainTusher/publish-please/internal/commits"
	"github.com/MdTanvirHossainTusher/publish-please/internal/semver"
)

// Strategy names, matching release-please's "versioning" option.
const (
	Default         = "default"
	AlwaysBumpPatch = "always-bump-patch"
	AlwaysBumpMinor = "always-bump-minor"
	AlwaysBumpMajor = "always-bump-major"
	Prerelease      = "prerelease"
	ServicePack     = "service-pack"
	DefaultInitial  = "1.0.0"
	defaultPreType  = "beta"
)

// Options controls how the next version is computed.
type Options struct {
	Versioning                string
	BumpMinorPreMajor         bool
	BumpPatchForMinorPreMajor bool
	// Prerelease forces a prerelease version (release-please "prerelease": true).
	Prerelease     bool
	PrereleaseType string
	// ReleaseAs pins the next version (config "release-as").
	ReleaseAs      string
	InitialVersion string
}

// Validate reports unknown strategy names.
func (o Options) Validate() error {
	switch o.Versioning {
	case "", Default, AlwaysBumpPatch, AlwaysBumpMinor, AlwaysBumpMajor, Prerelease, ServicePack:
		return nil
	}
	return fmt.Errorf("unknown versioning strategy %q", o.Versioning)
}

// BumpFor returns the default bump implied by changes.
func BumpFor(changes []commits.Change) semver.Bump {
	b := semver.BumpPatch
	for _, c := range changes {
		if c.Breaking {
			return semver.BumpMajor
		}
		if c.Type == "feat" {
			b = semver.BumpMinor
		}
	}
	return b
}

// Next returns the next version. current is nil when the package has never been released.
func Next(current *semver.Version, changes []commits.Change, o Options) (semver.Version, error) {
	if err := o.Validate(); err != nil {
		return semver.Version{}, err
	}
	// Explicit pins win: config first, then the newest Release-As footer.
	if o.ReleaseAs != "" {
		return semver.Parse(o.ReleaseAs)
	}
	for _, c := range changes { // changes are newest first
		if c.ReleaseAs != "" {
			return semver.Parse(c.ReleaseAs)
		}
	}
	if current == nil {
		init := o.InitialVersion
		if init == "" {
			init = DefaultInitial
		}
		v, err := semver.Parse(init)
		if err != nil {
			return v, err
		}
		if o.Prerelease || o.Versioning == Prerelease {
			if v.Prerelease == "" {
				v.Prerelease = preType(o) + ".1"
			}
		}
		return v, nil
	}

	cur := *current
	bump := BumpFor(changes)
	if cur.Major == 0 {
		if bump == semver.BumpMajor && o.BumpMinorPreMajor {
			bump = semver.BumpMinor
		} else if bump == semver.BumpMinor && o.BumpPatchForMinorPreMajor {
			bump = semver.BumpPatch
		}
	}

	switch o.Versioning {
	case AlwaysBumpPatch:
		return cur.Apply(semver.BumpPatch), nil
	case AlwaysBumpMinor:
		return cur.Apply(semver.BumpMinor), nil
	case AlwaysBumpMajor:
		return cur.Apply(semver.BumpMajor), nil
	case ServicePack:
		return servicePack(cur), nil
	case Prerelease:
		return nextPrerelease(cur, bump, preType(o)), nil
	}
	if o.Prerelease {
		return nextPrerelease(cur, bump, preType(o)), nil
	}
	if cur.IsPrerelease() {
		// Graduating a prerelease: 1.2.0-beta.3 -> 1.2.0 unless a bigger bump is needed.
		base := semver.Version{Major: cur.Major, Minor: cur.Minor, Patch: cur.Patch}
		if needsMoreThan(base, bump) {
			return base.Apply(bump), nil
		}
		return base, nil
	}
	return cur.Apply(bump), nil
}

func preType(o Options) string {
	if o.PrereleaseType != "" {
		return o.PrereleaseType
	}
	return defaultPreType
}

var preNumRe = regexp.MustCompile(`^(.*?)(\d+)$`)

// nextPrerelease bumps the prerelease counter when the current prerelease
// already covers the bump, otherwise starts a new prerelease line.
func nextPrerelease(cur semver.Version, bump semver.Bump, typ string) semver.Version {
	if cur.IsPrerelease() {
		base := semver.Version{Major: cur.Major, Minor: cur.Minor, Patch: cur.Patch}
		if !needsMoreThan(base, bump) {
			if m := preNumRe.FindStringSubmatch(cur.Prerelease); m != nil {
				n, _ := strconv.Atoi(m[2])
				num := fmt.Sprintf("%0*d", len(m[2]), n+1) // keep zero padding: alpha01 -> alpha02
				return semver.Version{Major: base.Major, Minor: base.Minor, Patch: base.Patch, Prerelease: m[1] + num}
			}
			return semver.Version{Major: base.Major, Minor: base.Minor, Patch: base.Patch, Prerelease: cur.Prerelease + ".1"}
		}
		next := base.Apply(bump)
		next.Prerelease = typ + ".1"
		return next
	}
	next := cur.Apply(bump)
	next.Prerelease = typ + ".1"
	return next
}

// needsMoreThan reports whether a prerelease of base cannot absorb bump.
// 2.0.0-x absorbs anything; 1.3.0-x absorbs minor/patch; 1.2.4-x absorbs patch.
func needsMoreThan(base semver.Version, bump semver.Bump) bool {
	switch bump {
	case semver.BumpMajor:
		return !(base.Minor == 0 && base.Patch == 0)
	case semver.BumpMinor:
		return base.Patch != 0
	}
	return false
}

var spRe = regexp.MustCompile(`^sp\.?(\d+)$`)

// servicePack produces 1.2.3-sp.1, 1.2.3-sp.2, ... (Java service-pack style).
func servicePack(cur semver.Version) semver.Version {
	n := 1
	if m := spRe.FindStringSubmatch(cur.Prerelease); m != nil {
		k, _ := strconv.Atoi(m[1])
		n = k + 1
	}
	return semver.Version{Major: cur.Major, Minor: cur.Minor, Patch: cur.Patch, Prerelease: "sp." + strconv.Itoa(n)}
}
