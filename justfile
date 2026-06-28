# cmd2host build tasks

# Default: show available recipes
default:
    @just --list

# Build for current platform
build:
    go build -o dist/cmd2host ./cmd/cmd2host

# Build for macOS Intel
build-darwin-amd64:
    GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o dist/cmd2host-darwin-amd64 ./cmd/cmd2host

# Build for macOS Apple Silicon
build-darwin-arm64:
    GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o dist/cmd2host-darwin-arm64 ./cmd/cmd2host

# Build all release binaries
build-all: build-darwin-amd64 build-darwin-arm64 \
    build-mcp-darwin-amd64 build-mcp-darwin-arm64 build-mcp-linux-amd64 build-mcp-linux-arm64 \
    build-proxy-darwin-amd64 build-proxy-darwin-arm64 build-proxy-linux-amd64 build-proxy-linux-arm64

# Build MCP server for current platform
build-mcp:
    go build -o dist/cmd2host-mcp ./cmd/cmd2host-mcp

# Build MCP server for macOS Intel
build-mcp-darwin-amd64:
    GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o dist/cmd2host-mcp-darwin-amd64 ./cmd/cmd2host-mcp

# Build MCP server for macOS Apple Silicon
build-mcp-darwin-arm64:
    GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o dist/cmd2host-mcp-darwin-arm64 ./cmd/cmd2host-mcp

# Build MCP server for Linux amd64 (for containers)
build-mcp-linux-amd64:
    GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dist/cmd2host-mcp-linux-amd64 ./cmd/cmd2host-mcp

# Build MCP server for Linux arm64 (for containers)
build-mcp-linux-arm64:
    GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o dist/cmd2host-mcp-linux-arm64 ./cmd/cmd2host-mcp

# Build cmd2host-proxy for current platform (container-side transparent proxy)
build-proxy:
    go build -o dist/cmd2host-proxy ./cmd/cmd2host-proxy

# Build cmd2host-proxy for macOS Intel
build-proxy-darwin-amd64:
    GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o dist/cmd2host-proxy-darwin-amd64 ./cmd/cmd2host-proxy

# Build cmd2host-proxy for macOS Apple Silicon
build-proxy-darwin-arm64:
    GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o dist/cmd2host-proxy-darwin-arm64 ./cmd/cmd2host-proxy

# Build cmd2host-proxy for Linux amd64 (primary container target)
build-proxy-linux-amd64:
    GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dist/cmd2host-proxy-linux-amd64 ./cmd/cmd2host-proxy

# Build cmd2host-proxy for Linux arm64 (ARM container target)
build-proxy-linux-arm64:
    GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o dist/cmd2host-proxy-linux-arm64 ./cmd/cmd2host-proxy

# Run unit tests for host daemon (root module: pkg/, cmd/, internal/)
test:
    go test -v ./...

# Run unit tests for MCP server
test-mcp:
    go test -v ./pkg/mcpserver/...

# Run host scenario tests (integration)
test-host: build
    ./test/host/run_tests.sh

# Run devcontainer feature test
test-devcontainer:
    devcontainer features test --features cmd2host --base-image mcr.microsoft.com/devcontainers/base:ubuntu

# Run E2E tests (daemon + devcontainer + MCP)
test-e2e:
    ./test/e2e/run_e2e.sh

# Run E2E tests with clean install (uninstall -> install)
test-e2e-clean:
    ./test/e2e/run_e2e.sh --clean

# Run E2E tests without rebuilding daemon
test-e2e-quick:
    ./test/e2e/run_e2e.sh --skip-build --skip-devcontainer

# Run E2E tests for Unix socket mode (no devcontainer needed)
test-e2e-unix:
    ./test/e2e/run_e2e_unix.sh

# Run all tests (except devcontainer and e2e)
test-all: test test-mcp test-host

# Clean build artifacts
clean:
    rm -rf dist/

# Install daemon locally (downloads from GitHub Releases)
install:
    ./host/scripts/install.sh

# Install daemon locally, building from source
install-build:
    ./host/scripts/install.sh --build

# Uninstall daemon
uninstall:
    ~/.cmd2host/uninstall.sh
