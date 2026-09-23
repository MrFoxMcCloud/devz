BIN     := devz
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build run install test vet fmt clean completions

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) .

# Dev builds run from the repo and never land on PATH, so the installed devz
# stays a release while the next version is in progress:
#   make run ARGS="doctor --quiet"
run: build
	./$(BIN) $(ARGS)

# Installs to GOBIN, or $(go env GOPATH)/bin. Make sure that is on your PATH.
# Only from a clean checkout of a release tag: installing a working tree is how
# an unreleased build ends up as the devz everything else calls. Equivalent to
#   go install github.com/MrFoxMcCloud/devz@vX.Y.Z
install:
	@git describe --tags --exact-match >/dev/null 2>&1 || { \
		echo "make install: HEAD is not a release tag; check out a vX.Y.Z tag, or use 'make run' for dev builds" >&2; exit 1; }
	@test -z "$$(git status --porcelain)" || { \
		echo "make install: working tree has uncommitted changes" >&2; exit 1; }
	go install -ldflags "$(LDFLAGS)" .

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# Regenerate the checked-in completion scripts from the built binary.
completions: build
	./$(BIN) completion zsh  > completions/_devz
	./$(BIN) completion bash > completions/devz.bash

clean:
	rm -f $(BIN)
	rm -rf dist/
