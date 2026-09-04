.PHONY: all build build-ui build-go run run-mcp status clean test test-race lint fmt vet tidy

# --- Configuration ---------------------------------------------------------
BIN_DIR       ?= bin
BINARY        ?= $(BIN_DIR)/wrongtrace
PKG           ?= ./cmd/wrongtrace
LDFLAGS       ?= -s -w -X main.version=dev
PORT          ?= 3444
WATCH_DIR     ?= .
REPO_NAME     ?= $(notdir $(CURDIR))
WRONGTRACE_HOME ?= $(HOME)/.wrongtrace
DB_PATH       ?= $(WRONGTRACE_HOME)/wrongtrace.db
SOCKET_PATH   ?= $(WRONGTRACE_HOME)/wrongtrace.sock

# --- Targets ---------------------------------------------------------------
all: build

build: build-ui build-go

# Build the React frontend into web/dist. Requires Node.js 20.19+/22.12+
# (vite 8 engines floor; Rolldown requires the newer V8/Node API surface).
build-ui:
	cd web && npm install && npm run build

# Compile the Go binary with the React assets embedded via //go:embed.
build-go:
	mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

# Run the observer daemon with sensible defaults.
run: build
	$(BINARY) start \
		--watch $(WATCH_DIR) \
		--port $(PORT) \
		--db $(DB_PATH) \
		--socket $(SOCKET_PATH) \
		--repo $(REPO_NAME)

# Spawn the MCP server over stdio (used by Claude Code, Cursor, etc.).
run-mcp: build
	$(BINARY) mcp

# Print a short status summary.
status: build
	$(BINARY) status

# --- Housekeeping -----------------------------------------------------------
test:
	go test ./...

# What CI actually gates on. Run this before pushing.
test-race:
	go test -race -count=1 ./...

# staticcheck finds what `go vet` misses: deprecated APIs, unreachable code,
# and dropped assignments. Installed on demand so a fresh clone needs no setup.
lint: vet
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...

fmt:
	go fmt ./...
	cd web && npx --yes prettier --write src

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf $(BIN_DIR) web/dist web/node_modules
