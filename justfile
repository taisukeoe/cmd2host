# cmd2host build tasks

# Default: show available recipes
default:
    @just --list

# Build for current platform
build:
    cd host && go build -o ../dist/cmd2host .

# Build for macOS Intel
build-darwin-amd64:
    cd host && GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o ../dist/cmd2host-darwin-amd64 .

# Build for macOS Apple Silicon
build-darwin-arm64:
    cd host && GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o ../dist/cmd2host-darwin-arm64 .

# Build all release binaries
build-all: build-darwin-amd64 build-darwin-arm64 build-mcp-darwin-amd64 build-mcp-darwin-arm64

# Build MCP server for current platform
build-mcp:
    cd mcp-server && go build -o ../dist/cmd2host-mcp .

# Build MCP server for macOS Intel
build-mcp-darwin-amd64:
    cd mcp-server && GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o ../dist/cmd2host-mcp-darwin-amd64 .

# Build MCP server for macOS Apple Silicon
build-mcp-darwin-arm64:
    cd mcp-server && GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o ../dist/cmd2host-mcp-darwin-arm64 .

# Build MCP server for Linux amd64 (for containers)
build-mcp-linux-amd64:
    cd mcp-server && GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o ../dist/cmd2host-mcp-linux-amd64 .

# Run unit tests for host daemon
test:
    cd host && go test -v ./...

# Run unit tests for MCP server
test-mcp:
    cd mcp-server && go test -v ./...

# Run host scenario tests (integration)
test-host: build
    ./test/host/run_tests.sh

# Run devcontainer feature test
test-devcontainer:
    devcontainer features test --features cmd2host --base-image mcr.microsoft.com/devcontainers/base:ubuntu

# Run all tests (except devcontainer)
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
