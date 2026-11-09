# Contributing to publish-please

Thanks for helping out. Bug reports, fixes, new release types and forge support are all welcome.

## Before you start

- For anything bigger than a small fix, open an issue first so we can agree on the approach.
- Check that the behaviour you want matches release-please where it makes sense. publish-please aims to read release-please config files unchanged.

## Setup

You need Go (see the version in `go.mod`) and, for end-to-end tests, Docker.

```sh
make test         # unit tests
make lint         # gofmt + go vet
make e2e          # full release cycle against a throwaway Gitea container
make e2e-forgejo  # the same against Forgejo
```

## Pull requests

- Use [Conventional Commits](https://www.conventionalcommits.org) for commit messages and PR titles (`feat: ...`, `fix(cli): ...`). publish-please releases itself from them, so they end up in the changelog.
- Add or update tests for the change.
- Keep `gofmt -l .` and `go vet ./...` clean; CI checks both.
- Don't edit `CHANGELOG.md` or version numbers by hand. The release PR does that.

## Releases

Releases are automated. Merging to `main` updates the release PR; merging the release PR tags the release, uploads binaries, pushes the Docker image and moves the `vN` major tag.

## Licence

By contributing you agree that your contributions are licensed under the [MIT License](LICENSE).
