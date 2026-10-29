# publish-please

Release pull requests, changelogs and releases from [Conventional Commits](https://www.conventionalcommits.org) — on **Gitea, Forgejo, GitHub and GitLab**.

publish-please does what [release-please](https://github.com/googleapis/release-please) does, but is not tied to GitHub. It reads the same `release-please-config.json` and `.release-please-manifest.json`, so an existing release-please setup works without changes.

```
 feat: add login  ─┐
 fix(api): crash  ─┼─►  release PR "chore(main): release 1.3.0"  ──merge──►  tag v1.3.0 + release notes
 docs: typo       ─┘     (version bumps + CHANGELOG.md)
```

## How it works

1. On every push to your release branch, publish-please reads the commits since the last release.
2. If there are releasable changes (`feat`, `fix`, `perf`, `revert`, `deps`, or anything breaking), it opens a **release PR** — or updates the open one — that bumps versions and prepends to `CHANGELOG.md`.
3. When you merge that PR, the next run **tags the merge commit and creates the release**, with the changelog entry as release notes.

The release PR is labelled `autorelease: pending`; after the release it becomes `autorelease: tagged`.

## Quick start

### Gitea / Forgejo Actions

```yaml
# .gitea/workflows/release.yml
name: release
on:
  push:
    branches: [main]
jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: https://github.com/MdTanvirHossainTusher/publish-please@v0.1.0
        id: release
        with:
          token: ${{ secrets.RELEASE_TOKEN }}
```

`RELEASE_TOKEN` is an access token with repository read/write. The built-in workflow token also works, but PRs opened with it do not trigger other workflows. No Docker on your runner? Install the binary instead — see [examples/gitea-actions.yml](examples/gitea-actions.yml).

### GitHub Actions

```yaml
permissions:
  contents: write
  pull-requests: write
jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: MdTanvirHossainTusher/publish-please@v0.1.0
        id: release
```

### GitLab CI

See [examples/gitlab-ci.yml](examples/gitlab-ci.yml). GitLab's job token cannot open merge requests, so use a project access token (`api`, `write_repository`) in `PUBLISH_PLEASE_TOKEN`.

### Command line

```sh
go install github.com/MdTanvirHossainTusher/publish-please/cmd/publish-please@latest

export PUBLISH_PLEASE_TOKEN=...
publish-please run --server-url https://gitea.example.com --repo team/app
publish-please release-pr --server-url https://gitea.example.com --repo team/app --dry-run
```

| Command      | What it does                                                               |
| ------------ | -------------------------------------------------------------------------- |
| `run`        | `release`, then `release-pr`. This is what CI normally runs.               |
| `release-pr` | Opens or updates the release PR. Closes it when nothing is left to release. |
| `release`    | Tags and releases every merged release PR that has not been released yet. |
| `bootstrap`  | Opens a PR adding a config file and manifest, seeded from your latest tag. |

### Setting up a repository

Either add a config file:

```sh
publish-please bootstrap --release-type node --server-url https://gitea.example.com --repo team/app
```

or skip the config entirely for a single package:

```sh
publish-please run --release-type simple
```

## Configuration

The config file is `publish-please-config.json` or `release-please-config.json`; the manifest is `.publish-please-manifest.json` or `.release-please-manifest.json`. Options at the top level are defaults for every package.

```json
{
  "release-type": "node",
  "bump-minor-pre-major": true,
  "plugins": ["node-workspace"],
  "packages": {
    ".": {},
    "packages/api": { "component": "api", "extra-files": ["src/version.ts"] },
    "charts/web": { "release-type": "helm", "exclude-paths": ["tests"] }
  }
}
```

```json
{ ".": "1.4.0", "packages/api": "0.9.2", "charts/web": "2.1.0" }
```

A package only sees commits that touch its own path. The root package sees every commit, minus its `exclude-paths`.

### Package options

| Option | Default | Description |
| --- | --- | --- |
| `release-type` | `simple` | See [release types](#release-types). |
| `package-name`, `component` | detected | Name used in tags (`api-v1.2.0`) and PR titles. |
| `changelog-path` | `CHANGELOG.md` | Relative to the package. |
| `changelog-sections` | release-please's | `[{"type": "feat", "section": "Features", "hidden": false}]` |
| `changelog-host` | forge URL | Base URL for links in notes. |
| `skip-changelog` | `false` | Don't write a changelog file. |
| `date-format` | `%Y-%m-%d` | strftime or Go layout. |
| `versioning` | `default` | `always-bump-patch`, `always-bump-minor`, `always-bump-major`, `prerelease`, `service-pack`. |
| `bump-minor-pre-major` | `false` | Breaking changes bump minor while < 1.0.0. |
| `bump-patch-for-minor-pre-major` | `false` | Features bump patch while < 1.0.0. |
| `prerelease`, `prerelease-type` | `false`, `beta` | `1.2.0-beta.1`, `1.2.0-beta.2`, … |
| `release-as` | | Pin the next version. Remove it after the release. |
| `initial-version` | `1.0.0` | First release when there is no previous one. |
| `include-component-in-tag` | `true` | `api-v1.2.0` vs `v1.2.0`. |
| `include-v-in-tag` | `true` | `v1.2.0` vs `1.2.0`. |
| `tag-separator` | `-` | `api-v1.2.0` vs `api/v1.2.0`. |
| `pull-request-title-pattern` | `chore${scope}: release${component} ${version}` | |
| `pull-request-header`, `pull-request-footer` | | Text around the notes in the PR body. |
| `draft-pull-request` | `false` | Open the release PR as a draft. |
| `draft` | `false` | Create draft releases (Gitea, GitHub). |
| `skip-github-release` / `skip-release` | `false` | Bump and tag nothing; only maintain the PR. |
| `exclude-paths` | | Paths (relative to the package) whose commits are ignored. |
| `extra-files` | | More files to bump; see below. |
| `version-file` | | File holding the version (simple, ruby, python, go). |

### Repository options (top level only)

| Option | Default | Description |
| --- | --- | --- |
| `separate-pull-requests` | `false` | One release PR per package instead of one combined PR. |
| `group-pull-request-title-pattern` | `chore: release ${branch}` | Title of the combined PR. |
| `label`, `release-label`, `extra-label` | `autorelease: pending`, `autorelease: tagged` | Comma-separated. |
| `bootstrap-sha`, `last-release-sha` | | Where to start reading history when there are no tags. |
| `plugins` | | `node-workspace`, `linked-versions`, `sentence-case`. |
| `always-update` | `false` | Push the release branch even when nothing changed. |
| `signoff` | | Adds `Signed-off-by:` to release commits. |
| `release-search-depth`, `commit-search-depth` | `400`, `500` | |

### Release types

| Type | Files updated |
| --- | --- |
| `simple` | `version.txt` (or `version-file`) |
| `node`, `expo` | `package.json`, `package-lock.json`, `npm-shrinkwrap.json` |
| `go` | changelog only (Go versions come from tags); `version-file` if set |
| `python` | `pyproject.toml` (`[project]` or `[tool.poetry]`), `setup.cfg`, `setup.py`, `<pkg>/__init__.py` |
| `rust` | `Cargo.toml`, `Cargo.lock` |
| `maven`, `java` | `pom.xml` (`/project/version`) |
| `gradle` | `gradle.properties`, `build.gradle`, `build.gradle.kts` |
| `helm` | `Chart.yaml` |
| `dart` | `pubspec.yaml` (keeps and increments the `+build` number) |
| `php` | `composer.json` |
| `ruby` | `lib/**/version.rb` (or `version-file`) |
| `elixir` | `mix.exs` |
| `terraform-module` | `README.md`, `versions.tf` |
| `dotnet` | `*.csproj`, `Directory.Build.props` |
| `bazel` | `MODULE.bazel` |
| `ocaml` | `*.opam`, `esy.json` |

Every updater edits only the version's bytes, so comments, key order and indentation stay exactly as they were.

### Extra files

```json
"extra-files": [
  "src/version.ts",
  { "type": "json", "path": "app.json", "jsonpath": "$.expo.version" },
  { "type": "yaml", "path": "/deploy/values.yaml", "jsonpath": "$.image.tag" },
  { "type": "toml", "path": "Config.toml", "jsonpath": "$.app.version" },
  { "type": "xml", "path": "Info.xml", "xpath": "/app/version" },
  { "type": "generic", "path": "docs/**/*.md", "glob": true }
]
```

A path starting with `/` is relative to the repository root. Plain strings (and `generic`) use markers:

```ts
export const VERSION = '1.2.3'; // x-release-please-version

// x-release-please-start-version
const docs = 'https://example.com/v1.2.3/';
// x-release-please-end
```

`x-release-please-major`, `-minor` and `-patch` replace a single number. The `x-publish-please-*` spelling works too.

### Plugins

- **`node-workspace`**: when a package is released, publish-please updates the dependency ranges in other workspace packages (`^1.0.0` → `^1.1.0`) and patch-releases them, cascading through the dependency graph. `workspace:` and `file:` ranges are left alone. Set `{"type": "node-workspace", "updatePeerDependencies": true}` to include peer dependencies.
- **`linked-versions`**: `{"type": "linked-versions", "groupName": "core", "components": ["api", "sdk"]}`. All listed components are released together, at the same version.
- **`sentence-case`**: capitalises the first letter of every changelog entry.

## Commit messages

```
feat(api): add pagination              -> minor
fix: handle empty body                 -> patch
perf: cache lookups                    -> patch
feat!: drop Node 16                    -> major
refactor: new parser

BREAKING CHANGE: `parse` is now async  -> major

chore: release 2.0.0

Release-As: 2.0.0                      -> exactly 2.0.0
```

**Several changes in one commit:** put extra headers after the body. Their footers are indented.

```
feat: add v4 UUIDs

fix(utils): unicode no longer throws
  BREAKING-CHANGE: encode() no longer throws.
feat(utils): encode supports unicode
```

**Fixing a squash-merge message:** put an override block in the PR description. It replaces that commit's message.

```
BEGIN_COMMIT_OVERRIDE
feat(core): real feature
fix: real fix
END_COMMIT_OVERRIDE
```

## Outputs

Written to `$GITHUB_OUTPUT` (Gitea, Forgejo and GitHub Actions) or to `--outputs-file` (for example a GitLab dotenv report). Use `--json` to print them.

| Output | Description |
| --- | --- |
| `releases_created` | `true` if any release was created |
| `release_created`, `tag_name`, `version`, `major`, `minor`, `patch`, `sha`, `html_url`, `upload_url` | the root package's release |
| `<path>--release_created`, `<path>--tag_name`, … | the same, per package path |
| `paths_released` | JSON array of released paths |
| `prs_created`, `pr`, `prs` | the release PR(s) as JSON |

## Flags and environment

| Flag | Environment | Notes |
| --- | --- | --- |
| `--token` | `PUBLISH_PLEASE_TOKEN`, `GITEA_TOKEN`, `GITLAB_TOKEN`, `GITHUB_TOKEN` | |
| `--forge` | `PUBLISH_PLEASE_FORGE` | Detected in Gitea, Forgejo, GitHub and GitLab CI; otherwise by probing the server. |
| `--server-url` | `GITEA_SERVER_URL`, `CI_SERVER_URL`, `GITHUB_SERVER_URL` | |
| `--api-url` | `CI_API_V4_URL`, `GITHUB_API_URL` | Derived from the server URL when empty. |
| `--repo` | `CI_PROJECT_PATH`, `GITHUB_REPOSITORY` | `owner/name`, or `group/subgroup/name` on GitLab. |
| `--target-branch` | | Defaults to the repository's default branch. |
| `--git-url` | | Clone URL, for when the API reports a URL the runner cannot reach. |
| `--repo-dir` | | Reuse an existing clone instead of a temporary one. Its working tree and index are never touched. |
| `--git-user-name`, `--git-user-email` | | Author of release commits. |
| `--dry-run`, `--debug`, `--json` | | |

## Differences from release-please

- **Works with any forge.** Git operations use plain git over HTTPS; everything else uses each forge's REST API.
- **Formatting is preserved.** JSON, YAML, TOML and XML files are edited in place rather than re-serialised.
- **No snapshot PRs.** Java `-SNAPSHOT` bumps after a release are not implemented.
- **Plugins.** `cargo-workspace`, `maven-workspace` and `group-priority` are not implemented yet; unknown plugins are ignored with a warning.
- **Root package component.** The root package only gets a component in its tag when `component` or `package-name` is set, so a single-package repo is tagged `v1.2.3`.
- **Release data in the PR.** The release PR carries a small hidden block saying what to tag, so `release` never has to guess from titles.

## Development

```sh
make test         # unit tests
make e2e          # full release cycle against a throwaway Gitea container (needs Docker)
make e2e-forgejo  # the same against Forgejo
make build
```

## License

[MIT](LICENSE)
