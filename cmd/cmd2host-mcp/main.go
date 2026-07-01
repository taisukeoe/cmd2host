// cmd2host-mcp is an MCP server that provides tools for interacting with
// the cmd2host daemon from AI agents running in containers. It is a thin
// wrapper around the pkg/mcpserver library: flag parsing here, the actual
// MCP server runtime there.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/taisukeoe/cmd2host/pkg/mcpserver"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	var (
		daemonHost  = flag.String("host", "host.docker.internal", "cmd2host daemon host (TCP mode)")
		daemonPort  = flag.Int("port", 9876, "cmd2host daemon port (TCP mode)")
		socketPath  = flag.String("socket", "", "cmd2host daemon socket path (Unix mode, overrides host/port)")
		tokenFile   = flag.String("token-file", "", "Path to file containing authentication token")
		showVersion = flag.Bool("version", false, "Show version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("cmd2host-mcp version %s\n", version)
		os.Exit(0)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Token must arrive via -token-file or the CMD2HOST_TOKEN environment
	// variable so the raw session token never enters the process argv.
	// Callers embedding mcpserver.Run in-process can still pass a raw
	// value via Options.Token; the flag surface deliberately does not.
	opts := mcpserver.Options{
		DaemonHost: *daemonHost,
		DaemonPort: *daemonPort,
		SocketPath: *socketPath,
		TokenFile:  *tokenFile,
		Version:    version,
	}

	if err := mcpserver.Run(ctx, opts); err != nil {
		if errors.Is(err, context.Canceled) {
			// Caller-initiated shutdown (SIGINT / SIGTERM): exit cleanly.
			return
		}
		if errors.Is(err, mcpserver.ErrTokenRequired) {
			log.Print("cmd2host-mcp: token is required. Use -token-file or set CMD2HOST_TOKEN.")
			os.Exit(1)
		}
		log.Printf("cmd2host-mcp: %v", err)
		os.Exit(1)
	}
}
