BIN     := devz
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test vet fmt clean completions

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) .

# Installs to GOBIN, or $(go env GOPATH)/bin. Make sure that is on your PATH.
install:
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
