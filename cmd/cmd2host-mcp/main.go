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
		token       = flag.String("token", "", "Authentication token (deprecated; use -token-file or the CMD2HOST_TOKEN environment variable)")
		tokenFile   = flag.String("token-file", "", "Path to file containing authentication token")
		showVersion = flag.Bool("version", false, "Show version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("cmd2host-mcp version %s\n", version)
		os.Exit(0)
	}

	if *token != "" {
		// Kept parseable for compatibility; new setups should not use this
		// path. The recommended inputs are -token-file and CMD2HOST_TOKEN,
		// both of which are documented in the README and the DevContainer
		// mcp.json template.
		fmt.Fprintln(os.Stderr, "cmd2host-mcp: -token is deprecated and will be removed in a future release; use -token-file or the CMD2HOST_TOKEN environment variable instead.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := mcpserver.Options{
		DaemonHost: *daemonHost,
		DaemonPort: *daemonPort,
		SocketPath: *socketPath,
		Token:      *token,
		TokenFile:  *tokenFile,
		Version:    version,
	}

	if err := mcpserver.Run(ctx, opts); err != nil {
		if errors.Is(err, context.Canceled) {
			// Caller-initiated shutdown (SIGINT / SIGTERM): exit cleanly.
			return
		}
		if errors.Is(err, mcpserver.ErrTokenRequired) {
			log.Print("cmd2host-mcp: token is required. Use -token, -token-file, or set CMD2HOST_TOKEN.")
			os.Exit(1)
		}
		log.Printf("cmd2host-mcp: %v", err)
		os.Exit(1)
	}
}
