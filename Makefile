BIN     := devz
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test vet fmt clean completions

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) .

# Installs the working tree to GOBIN, or $(go env GOPATH)/bin, replacing the
# devz on PATH. Return to a release with
#   go install github.com/MrFoxMcCloud/devz@v1
# or keep both: `make install BIN=devz2` installs the work in progress beside
# it. `devz version` says which one you are running.
GOBIN_DIR := $(or $(shell go env GOBIN),$(shell go env GOPATH)/bin)

install:
	@case "$(BIN)" in devz-*) \
		echo "make install: $(BIN) would be picked up as a devz plugin; pick a name without the devz- prefix" >&2; exit 1;; esac
	go build -ldflags "$(LDFLAGS)" -o "$(GOBIN_DIR)/$(BIN)" .

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
