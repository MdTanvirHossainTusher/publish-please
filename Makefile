VERSION ?= dev
LDFLAGS := -s -w -X github.com/MdTanvirHossainTusher/publish-please/internal/cli.Version=$(VERSION)

.PHONY: build test e2e e2e-forgejo lint docker clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/publish-please ./cmd/publish-please

test:
	go test ./...

# End-to-end tests against a throwaway Gitea / Forgejo container (needs Docker).
e2e:
	scripts/e2e-gitea.sh

e2e-forgejo:
	IMAGE=codeberg.org/forgejo/forgejo:11 scripts/e2e-gitea.sh

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...

docker:
	docker build --build-arg VERSION=$(VERSION) -t publish-please:$(VERSION) .

clean:
	rm -rf bin dist
