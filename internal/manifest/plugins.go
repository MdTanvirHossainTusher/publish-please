package manifest

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/MdTanvirHossainTusher/publish-please/internal/config"
	"github.com/MdTanvirHossainTusher/publish-please/internal/semver"
	"github.com/MdTanvirHossainTusher/publish-please/internal/updaters"
)

// linkedVersions gives every component in the group the highest next
// version, releasing members even when they have no changes.
func (r *Runner) linkedVersions(pl config.Plugin, states map[string]*state, cands []*Candidate) ([]*Candidate, error) {
	members := map[string]bool{}
	for _, c := range pl.Components {
		members[c] = true
	}
	var max *semver.Version
	inGroup := map[string]*Candidate{}
	for _, c := range cands {
		if members[c.Component] {
			inGroup[c.Pkg.Path] = c
			if max == nil || semver.Compare(c.Version, *max) > 0 {
				v := c.Version
				max = &v
			}
		}
	}
	if max == nil {
		return cands, nil // nothing in the group is releasing
	}
	for p, st := range states {
		if !members[st.component] {
			continue
		}
		if st.prev != nil && semver.Compare(*max, *st.prev) <= 0 {
			continue
		}
		if c, ok := inGroup[p]; ok {
			c.Version = *max
			continue
		}
		c := st.candidate(*max)
		cands = append(cands, c)
		inGroup[p] = c
	}
	r.logf("linked-versions %q: %d components released as %s", pl.GroupName, len(inGroup), max)
	return cands, nil
}

var depSections = []string{"dependencies", "devDependencies", "optionalDependencies"}

var rangePrefixRe = regexp.MustCompile(`^(\^|~|>=|<=|>|<|=)?`)

type nodePkg struct {
	path    string
	name    string
	content string
}

// nodeWorkspace updates dependency ranges between workspace packages and
// patch-releases dependents of released packages (cascading).
func (r *Runner) nodeWorkspace(ctx context.Context, pl config.Plugin, states map[string]*state, cands []*Candidate) ([]*Candidate, error) {
	sections := depSections
	if pl.UpdatePeerDependencies {
		sections = append(append([]string{}, depSections...), "peerDependencies")
	}
	var pkgs []nodePkg
	for _, p := range r.Config.Paths() {
		pkg := r.Config.Packages[p]
		if pkg.ReleaseType != "node" && pkg.ReleaseType != "expo" {
			continue
		}
		c, ok, err := r.Repo.ReadFile(ctx, r.head, config.Join(p, "package.json"))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		var pj struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(c), &pj); err != nil {
			return nil, fmt.Errorf("%s/package.json: %w", p, err)
		}
		pkgs = append(pkgs, nodePkg{path: p, name: pj.Name, content: c})
	}
	byPath := map[string]*Candidate{}
	for _, c := range cands {
		byPath[c.Pkg.Path] = c
	}
	released := func() map[string]*Candidate {
		m := map[string]*Candidate{}
		for _, np := range pkgs {
			if c, ok := byPath[np.path]; ok && np.name != "" {
				m[np.name] = c
			}
		}
		return m
	}

	// Phase 1: find dependents that must be released, until nothing changes.
	for changed := true; changed; {
		changed = false
		rel := released()
		for _, np := range pkgs {
			if _, ok := byPath[np.path]; ok {
				continue
			}
			if len(depUpdates(np, rel, sections)) == 0 {
				continue
			}
			st := states[np.path]
			if st == nil {
				continue
			}
			base := semver.Version{}
			if st.prev != nil {
				base = *st.prev
			}
			c := st.candidate(base.Apply(semver.BumpPatch))
			if st.prev == nil {
				c.Version = semver.MustParse("1.0.0")
			}
			cands = append(cands, c)
			byPath[np.path] = c
			changed = true
		}
	}

	// Phase 2: write the range updates with the final versions.
	rel := released()
	for _, np := range pkgs {
		c, ok := byPath[np.path]
		if !ok {
			continue
		}
		ups := depUpdates(np, rel, sections)
		if len(ups) == 0 {
			continue
		}
		bySection := map[string][]string{}
		for _, u := range ups {
			c.ExtraUpdates = append(c.ExtraUpdates, updaters.FileUpdate{
				Path:    config.Join(np.path, "package.json"),
				Updater: updaters.JSONField(fmt.Sprintf(`$["%s"]["%s"]`, u.section, u.dep), u.to),
			})
			bySection[u.section] = append(bySection[u.section], fmt.Sprintf("    * %s bumped from %s to %s", u.dep, u.from, u.to))
		}
		notes := []string{"* The following workspace dependencies were updated"}
		secs := make([]string, 0, len(bySection))
		for s := range bySection {
			secs = append(secs, s)
		}
		sort.Strings(secs)
		for _, s := range secs {
			notes = append(notes, "  * "+s)
			notes = append(notes, bySection[s]...)
		}
		c.DepNotes = append(c.DepNotes, notes...)
	}
	return cands, nil
}

type depUpdate struct {
	section, dep, from, to string
}

func depUpdates(np nodePkg, rel map[string]*Candidate, sections []string) []depUpdate {
	var out []depUpdate
	for _, sec := range sections {
		for name, c := range rel {
			if name == np.name {
				continue
			}
			cur, ok := updaters.GetJSONString(np.content, updaters.Keys(sec, name))
			if !ok || strings.Contains(cur, ":") || cur == "*" {
				continue // workspace:, file:, link:, npm: protocols and wildcards stay
			}
			prefix := rangePrefixRe.FindString(cur)
			next := prefix + c.Version.String()
			if next != cur {
				out = append(out, depUpdate{section: sec, dep: name, from: cur, to: next})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].section != out[j].section {
			return out[i].section < out[j].section
		}
		return out[i].dep < out[j].dep
	})
	return out
}
