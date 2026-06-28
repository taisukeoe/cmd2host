// cmd2host-proxy is the container-side transparent proxy binary for the
// cmd2host daemon. Installed as a single binary plus per-command symlinks
// for auth-heavy CLIs the project config declares operation templates for
// (gh, aws, gcloud, ...), it argv[0]-dispatches the user's invocation into
// pkg/proxyclient.Dispatch which:
//
//   - runs container-side early-reject checks (stdin / file:// /
//     TTY-required subcommand);
//   - posts a raw_argv operation request to the daemon via TCP or Unix
//     socket, carrying the session token loaded from -token-file;
//   - copies the daemon's response stdout / stderr onto the caller's
//     streams and exits with either the host command's exit code (0..127)
//     or one of the reserved 200 / 201 / 220 / 230 bands. See
//     pkg/proxyclient/dispatch.go for the exit-code policy.
//
// The binary is intentionally thin — flag/env parsing here, behaviour in
// the library — so the same library can be unit-tested without process
// state.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/taisukeoe/cmd2host/pkg/proxyclient"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

// Environment variables consumed when the corresponding flag is not set.
// Names match the container-side conventions used by entrypoint.sh and
// devcontainer-feature.json (HOST_CMD_PROXY_HOST/PORT/SOCKET/TOKEN_FILE),
// so existing setups need no env changes to switch from the legacy
// cmd-wrapper.sh to cmd2host-proxy.
const (
	envHost        = "HOST_CMD_PROXY_HOST"
	envPort        = "HOST_CMD_PROXY_PORT"
	envSocket      = "HOST_CMD_PROXY_SOCKET"
	envTokenFile   = "HOST_CMD_PROXY_TOKEN_FILE"
	envTargetRepo  = "HOST_CMD_PROXY_TARGET_REPO"
	defaultHost    = "host.docker.internal"
	defaultPort    = 9876
	defaultTokFile = "/run/cmd2host-token"
)

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

// run is split out of main so the exit-code path can be unit-tested.
// argv corresponds to os.Args (full, including argv[0]).
func run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		fmt.Fprintln(stderr, "cmd2host-proxy: empty argv")
		return proxyclient.ExitInfrastructure
	}

	// Symlink form (argv[0] basename is gh / aws / ...) skips wrapper
	// flag parsing entirely so the host command's flags (e.g.
	// `gh --version`, `gh -R owner/repo`) are not mis-interpreted as
	// wrapper flags. Direct form (argv[0] basename starts with
	// "cmd2host-proxy") parses wrapper-owned flags first and may exit
	// early on `--version`; only then does it require a host command.
	//
	// `strings.HasPrefix` instead of exact equality so renamed binary
	// artefacts (`cmd2host-proxy-v0.3.0` release asset,
	// `cmd2host-proxy.bak` after a manual upgrade) still hit the direct
	// branch. The trade-off is that a user-created symlink whose name
	// itself starts with "cmd2host-proxy" gets treated as direct
	// invocation; the failure mode is loud (unknown flag rather than
	// silent dispatch).
	base := filepath.Base(argv[0])
	if !strings.HasPrefix(base, "cmd2host-proxy") {
		// Symlink form: env-resolved defaults are used; per-invocation
		// flag overrides are not available on this path.
		return dispatch(base, argv[1:],
			envOr(envHost, defaultHost),
			envIntOr(envPort, defaultPort),
			os.Getenv(envSocket),
			envOr(envTokenFile, defaultTokFile),
			os.Getenv(envTargetRepo),
			stdout, stderr,
		)
	}

	// Direct form: parse wrapper-owned flags before requiring a host
	// command. `--version` exits 0 here, ahead of the missing-command
	// check, so `cmd2host-proxy --version` works as documented.
	fs := flag.NewFlagSet("cmd2host-proxy", flag.ContinueOnError)
	fs.SetOutput(stderr)

	hostFlag := fs.String("host", envOr(envHost, defaultHost), "cmd2host daemon host (TCP mode)")
	portFlag := fs.Int("port", envIntOr(envPort, defaultPort), "cmd2host daemon port (TCP mode)")
	socketFlag := fs.String("socket", os.Getenv(envSocket), "cmd2host daemon Unix socket path (overrides host/port)")
	tokenFileFlag := fs.String("token-file", envOr(envTokenFile, defaultTokFile), "Path to the session token file")
	targetRepoFlag := fs.String("target-repo", os.Getenv(envTargetRepo), "Target repo override (defaults to the project's primary repo)")
	showVersion := fs.Bool("version", false, "Show version and exit")

	if err := fs.Parse(argv[1:]); err != nil {
		// fs.Parse already wrote a usage message to stderr.
		return proxyclient.ExitInfrastructure
	}

	if *showVersion {
		fmt.Fprintf(stdout, "cmd2host-proxy version %s\n", version)
		return 0
	}

	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "cmd2host-proxy: missing host command")
		fs.Usage()
		return proxyclient.ExitInfrastructure
	}

	return dispatch(rest[0], rest[1:],
		*hostFlag, *portFlag, *socketFlag, *tokenFileFlag, *targetRepoFlag,
		stdout, stderr,
	)
}

// dispatch loads the token file, constructs a proxyclient.Client, and
// hands off to proxyclient.Dispatch. Extracted from run() so the symlink
// and direct invocation branches share one execution path once the
// command + argv pair is resolved.
func dispatch(command string, hostArgs []string, host string, port int, socket, tokenFile, targetRepo string, stdout, stderr io.Writer) int {
	token, terr := readTokenFile(tokenFile)
	if terr != nil {
		fmt.Fprintln(stderr, "cmd2host: "+terr.Error()+"; run mcp__cmd2host__cmd2host_list_operations to discover supported operations")
		return proxyclient.ExitTokenRead
	}

	client := &proxyclient.Client{
		Host:       host,
		Port:       port,
		SocketPath: socket,
		Token:      token,
	}

	return proxyclient.Dispatch(proxyclient.Options{
		Command:    command,
		Argv:       hostArgs,
		Client:     client,
		TargetRepo: targetRepo,
		Stdout:     stdout,
		Stderr:     stderr,
	})
}

// readTokenFile reads and trims a session token from the given path.
// Empty content is treated as a configuration error so the dispatch
// layer can return ExitTokenRead rather than sending an empty token to
// the daemon (which would trigger the 1-second brute-force-protection
// delay).
func readTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file %q: %w", path, err)
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", fmt.Errorf("token file %q is empty", path)
	}
	return tok, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envIntOr(name string, fallback int) int {
	v := os.Getenv(name)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
