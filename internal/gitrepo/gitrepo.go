// Package gitrepo reads history and writes commits with git plumbing.
//
// It never touches a working tree or the index of the repository it runs in:
// fetched refs live under refs/publish-please/ and commits are assembled with
// a temporary index file, then pushed straight to the remote.
package gitrepo

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MdTanvirHossainTusher/publish-please/internal/commits"
)

// Namespace holds every ref this tool fetches.
const Namespace = "refs/publish-please"

// Auth describes HTTP basic credentials sent to the git remote.
type Auth struct {
	Username string
	Password string
}

// Repo is a local git repository used as scratch space.
type Repo struct {
	Dir       string
	RemoteURL string
	auth      *Auth
	temp      bool
	Logf      func(format string, a ...any)
}

// Open uses an existing repository (bare or not) at dir.
func Open(dir, remoteURL string, auth *Auth) (*Repo, error) {
	r := &Repo{Dir: dir, RemoteURL: remoteURL, auth: auth}
	if _, err := r.git(context.Background(), nil, "rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("%s is not a git repository: %w", dir, err)
	}
	return r, nil
}

// Temp creates an empty bare repository in a temp dir. Call Close to remove it.
func Temp(remoteURL string, auth *Auth) (*Repo, error) {
	dir, err := os.MkdirTemp("", "publish-please-")
	if err != nil {
		return nil, err
	}
	r := &Repo{Dir: dir, RemoteURL: remoteURL, auth: auth, temp: true}
	ctx := context.Background()
	if _, err := r.git(ctx, nil, "init", "--bare", "-q"); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	// Partial clone: commits and trees now, file contents on demand.
	for _, kv := range [][2]string{
		{"remote.origin.url", remoteURL},
		{"remote.origin.promisor", "true"},
		{"remote.origin.partialclonefilter", "blob:none"},
		{"extensions.partialclone", "origin"},
		{"core.repositoryformatversion", "1"},
	} {
		if _, err := r.git(ctx, nil, "config", kv[0], kv[1]); err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
	}
	return r, nil
}

// Close removes a temporary repository.
func (r *Repo) Close() error {
	if r.temp {
		return os.RemoveAll(r.Dir)
	}
	return nil
}

func (r *Repo) logf(format string, a ...any) {
	if r.Logf != nil {
		r.Logf(format, a...)
	}
}

func (r *Repo) env(extra []string) []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	if r.auth != nil && r.auth.Password != "" {
		cred := base64.StdEncoding.EncodeToString([]byte(r.auth.Username + ":" + r.auth.Password))
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+cred,
		)
	}
	return append(env, extra...)
}

func (r *Repo) git(ctx context.Context, stdin []byte, args ...string) (string, error) {
	return r.gitEnv(ctx, nil, stdin, args...)
}

func (r *Repo) gitEnv(ctx context.Context, extraEnv []string, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	cmd.Env = r.env(extraEnv)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if r.auth != nil && r.auth.Password != "" {
			msg = strings.ReplaceAll(msg, r.auth.Password, "***")
		}
		return out.String(), fmt.Errorf("git %s: %w: %s", args[0], err, msg)
	}
	return out.String(), nil
}

// BranchRef is where a fetched remote branch is stored locally.
func BranchRef(branch string) string { return Namespace + "/heads/" + branch }

// TagRef is where a fetched remote tag is stored locally.
func TagRef(tag string) string { return Namespace + "/tags/" + tag }

// Fetch downloads a branch and all tags from the remote.
func (r *Repo) Fetch(ctx context.Context, branch string) error {
	if shallow, _ := r.git(ctx, nil, "rev-parse", "--is-shallow-repository"); strings.TrimSpace(shallow) == "true" {
		r.logf("repository is shallow, fetching full history")
		if _, err := r.git(ctx, nil, "fetch", "--unshallow", "--no-tags", r.RemoteURL, "+refs/heads/"+branch+":"+BranchRef(branch)); err != nil {
			return err
		}
	}
	args := []string{"fetch", "--no-tags", "--force", "--prune"}
	if r.temp {
		args = append(args, "--filter=blob:none")
	}
	args = append(args, r.RemoteURL,
		"+refs/heads/"+branch+":"+BranchRef(branch),
		"+refs/tags/*:"+Namespace+"/tags/*",
	)
	_, err := r.git(ctx, nil, args...)
	return err
}

// FetchBranch fetches a single branch. A missing branch is not an error.
func (r *Repo) FetchBranch(ctx context.Context, branch string) (bool, error) {
	args := []string{"fetch", "--no-tags", "--force"}
	if r.temp {
		args = append(args, "--filter=blob:none")
	}
	args = append(args, r.RemoteURL, "+refs/heads/"+branch+":"+BranchRef(branch))
	if _, err := r.git(ctx, nil, args...); err != nil {
		if strings.Contains(err.Error(), "couldn't find remote ref") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Resolve returns the commit SHA a ref points to, or "" if it does not exist.
func (r *Repo) Resolve(ctx context.Context, ref string) string {
	out, err := r.git(ctx, nil, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Tags returns every fetched tag name mapped to its commit SHA.
func (r *Repo) Tags(ctx context.Context) (map[string]string, error) {
	out, err := r.git(ctx, nil, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(*objectname)", Namespace+"/tags/")
	if err != nil {
		return nil, err
	}
	tags := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l == "" {
			continue
		}
		p := strings.Split(l, "\x00")
		name := strings.TrimPrefix(p[0], Namespace+"/tags/")
		sha := p[1]
		if len(p) > 2 && p[2] != "" { // annotated tag: use the peeled commit
			sha = p[2]
		}
		tags[name] = sha
	}
	return tags, nil
}

// IsAncestor reports whether a is an ancestor of (or equal to) b.
func (r *Repo) IsAncestor(ctx context.Context, a, b string) bool {
	_, err := r.git(ctx, nil, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

// LogOptions selects commits.
type LogOptions struct {
	From     string   // exclusive; "" means from the beginning
	To       string   // inclusive
	Paths    []string // limit to these paths (pathspec)
	Excludes []string // drop commits that only touch these
	Max      int
}

// Log returns commits newest first, each with the files it touched.
func (r *Repo) Log(ctx context.Context, o LogOptions) ([]commits.Commit, error) {
	args := []string{"log", "--format=%x1e%H%x00%B%x00", "--name-only", "--no-renames", "-c"}
	if o.Max > 0 {
		args = append(args, fmt.Sprintf("--max-count=%d", o.Max))
	}
	rng := o.To
	if o.From != "" {
		rng = o.From + ".." + o.To
	}
	args = append(args, rng, "--")
	spec := false
	for _, p := range o.Paths {
		if p != "" && p != "." {
			args = append(args, p)
			spec = true
		}
	}
	if len(o.Excludes) > 0 {
		if !spec {
			args = append(args, ".")
		}
		for _, e := range o.Excludes {
			args = append(args, ":(exclude)"+e)
		}
	}
	out, err := r.git(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	var cs []commits.Commit
	for _, rec := range strings.Split(out, "\x1e") {
		if strings.TrimSpace(rec) == "" {
			continue
		}
		p := strings.SplitN(rec, "\x00", 3)
		if len(p) < 2 {
			continue
		}
		c := commits.Commit{SHA: strings.TrimSpace(p[0]), Message: strings.TrimSpace(p[1])}
		if len(p) == 3 {
			for _, f := range strings.Split(p[2], "\n") {
				if f = strings.TrimSpace(f); f != "" {
					c.Files = append(c.Files, f)
				}
			}
		}
		cs = append(cs, c)
	}
	return cs, nil
}

// ReadFile returns a file's content at ref. ok is false when it does not exist.
func (r *Repo) ReadFile(ctx context.Context, ref, path string) (string, bool, error) {
	out, err := r.git(ctx, nil, "cat-file", "-p", ref+":"+path)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "Not a valid object name") || strings.Contains(err.Error(), "exists on disk, but not in") {
			return "", false, nil
		}
		return "", false, err
	}
	return out, true, nil
}

// ListFiles returns every file path at ref, optionally under a directory.
func (r *Repo) ListFiles(ctx context.Context, ref, dir string) ([]string, error) {
	args := []string{"ls-tree", "-r", "--name-only", ref}
	if dir != "" && dir != "." {
		args = append(args, "--", dir)
	}
	out, err := r.git(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\n") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// TreeOf returns the tree SHA of a commit.
func (r *Repo) TreeOf(ctx context.Context, commit string) string {
	out, err := r.git(ctx, nil, "rev-parse", commit+"^{tree}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Identity is a commit author/committer.
type Identity struct {
	Name  string
	Email string
}

// Commit writes a new commit on top of parent with files replaced by the
// given contents. It returns the new commit SHA.
func (r *Repo) Commit(ctx context.Context, parent string, files map[string]string, message string, who Identity) (string, error) {
	idx, err := os.CreateTemp("", "publish-please-index-")
	if err != nil {
		return "", err
	}
	idxPath := idx.Name()
	idx.Close()
	os.Remove(idxPath) // git wants to create it itself
	defer os.Remove(idxPath)
	env := []string{"GIT_INDEX_FILE=" + idxPath}

	if _, err := r.gitEnv(ctx, env, nil, "read-tree", parent); err != nil {
		return "", err
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		blob, err := r.git(ctx, []byte(files[p]), "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		mode := "100644"
		if out, err := r.git(ctx, nil, "ls-tree", parent, "--", p); err == nil && strings.TrimSpace(out) != "" {
			mode = strings.Fields(out)[0]
		}
		info := fmt.Sprintf("%s,%s,%s", mode, strings.TrimSpace(blob), filepath.ToSlash(p))
		if _, err := r.gitEnv(ctx, env, nil, "update-index", "--add", "--cacheinfo", info); err != nil {
			return "", err
		}
	}
	tree, err := r.gitEnv(ctx, env, nil, "write-tree")
	if err != nil {
		return "", err
	}
	idEnv := []string{
		"GIT_AUTHOR_NAME=" + who.Name, "GIT_AUTHOR_EMAIL=" + who.Email,
		"GIT_COMMITTER_NAME=" + who.Name, "GIT_COMMITTER_EMAIL=" + who.Email,
	}
	sha, err := r.gitEnv(ctx, idEnv, []byte(message), "commit-tree", strings.TrimSpace(tree), "-p", parent, "-F", "-")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sha), nil
}

// Push force-updates a remote branch to sha.
func (r *Repo) Push(ctx context.Context, sha, branch string) error {
	_, err := r.git(ctx, nil, "push", "--force", "--no-verify", r.RemoteURL, sha+":refs/heads/"+branch)
	return err
}
