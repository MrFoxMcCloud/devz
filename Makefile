BIN     := devz
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test vet fmt clean completions

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) .

# Installs the working tree as ./$(BIN) in this checkout, never onto PATH, so
# the released devz that launchers and MCP servers call is never replaced by
# work in progress. Run it as ./devz, or by its full path from another repo.
# The release on PATH comes only from
#   go install github.com/MrFoxMcCloud/devz@v1
install:
	@case "$(BIN)" in devz-*) \
		echo "make install: $(BIN) would be picked up as a devz plugin; pick a name without the devz- prefix" >&2; exit 1;; esac
	go build -ldflags "$(LDFLAGS)" -o "./$(BIN)" .

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
