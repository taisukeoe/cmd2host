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
build-all: build-darwin-amd64 build-darwin-arm64

# Run unit tests
test:
    cd host && go test -v ./...

# Run host scenario tests (integration)
test-host: build
    ./test/host/run_tests.sh

# Run devcontainer feature test
test-devcontainer:
    devcontainer features test --features cmd2host --base-image mcr.microsoft.com/devcontainers/base:ubuntu

# Run all tests (except devcontainer)
test-all: test test-host

# Clean build artifacts
clean:
    rm -rf dist/
