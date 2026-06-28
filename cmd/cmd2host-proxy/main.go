// cmd2host-proxy is the container-side transparent proxy binary for the
// cmd2host daemon. Installed as a single binary plus per-command symlinks
// (gh, git, aws, ...), it argv[0]-dispatches the user's invocation into
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
func run(argv []string, stdout, stderr *os.File) int {
	// Strip wrapper-owned flags from a copy of argv before extracting
	// the host command. The remaining args (after FlagSet.Parse) are
	// what the user wanted to send to gh / git / aws.
	fs := flag.NewFlagSet("cmd2host-proxy", flag.ContinueOnError)
	fs.SetOutput(stderr)

	hostFlag := fs.String("host", envOr(envHost, defaultHost), "cmd2host daemon host (TCP mode)")
	portFlag := fs.Int("port", envIntOr(envPort, defaultPort), "cmd2host daemon port (TCP mode)")
	socketFlag := fs.String("socket", os.Getenv(envSocket), "cmd2host daemon Unix socket path (overrides host/port)")
	tokenFileFlag := fs.String("token-file", envOr(envTokenFile, defaultTokFile), "Path to the session token file")
	targetRepoFlag := fs.String("target-repo", os.Getenv(envTargetRepo), "Target repo override (defaults to the project's primary repo)")
	showVersion := fs.Bool("version", false, "Show version and exit")

	// Wrapper-owned flags must be split from the host command's flags.
	// When invoked via symlink (argv[0] is gh / git / aws / ...), the
	// wrapper has no flags of its own to parse — everything after
	// argv[0] belongs to the host command. The direct-invocation form
	// (`cmd2host-proxy [flags] <command> [args...]`) is the only one
	// that uses fs.Parse.
	command, hostArgs, err := splitArgvByInvocation(argv, fs)
	if err != nil {
		// fs.Parse already wrote a usage message to stderr.
		return proxyclient.ExitInfrastructure
	}

	if *showVersion {
		fmt.Fprintf(stdout, "cmd2host-proxy version %s\n", version)
		return 0
	}

	token, terr := readTokenFile(*tokenFileFlag)
	if terr != nil {
		fmt.Fprintln(stderr, "cmd2host: "+terr.Error()+"; run mcp__cmd2host__cmd2host_list_operations to discover supported operations")
		return proxyclient.ExitTokenRead
	}

	client := &proxyclient.Client{
		Host:       *hostFlag,
		Port:       *portFlag,
		SocketPath: *socketFlag,
		Token:      token,
	}

	return proxyclient.Dispatch(proxyclient.Options{
		Command:    command,
		Argv:       hostArgs,
		Client:     client,
		TargetRepo: *targetRepoFlag,
		Stdout:     stdout,
		Stderr:     stderr,
	})
}

// splitArgvByInvocation distinguishes the symlink invocation form
// (argv[0] is gh / git / aws / etc.) from the direct invocation form
// (argv[0] basename is cmd2host-proxy). Returns the host command name
// and the host command's argv tail.
//
// In direct form, the caller may pass wrapper-owned flags between
// "cmd2host-proxy" and the host command name, e.g.:
//
//	cmd2host-proxy -host=10.0.0.5 -port=9876 gh pr view 42
//
// fs.Parse consumes the wrapper flags up to the first non-flag
// positional, which becomes the host command. Everything after it is
// the host command's argv. In symlink form, fs.Parse is skipped
// entirely so the host command's flags (e.g. `-R owner/repo`) are not
// mis-interpreted as wrapper flags.
func splitArgvByInvocation(argv []string, fs *flag.FlagSet) (command string, hostArgs []string, err error) {
	if len(argv) == 0 {
		return "", nil, fmt.Errorf("no argv")
	}
	base := filepath.Base(argv[0])
	if base != "cmd2host-proxy" {
		// Symlink form: argv[0] is the host command, the rest is its argv.
		return base, argv[1:], nil
	}
	// Direct form: parse wrapper flags, then peel off the host command.
	if err := fs.Parse(argv[1:]); err != nil {
		return "", nil, err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(fs.Output(), "cmd2host-proxy: missing host command")
		fs.Usage()
		return "", nil, fmt.Errorf("missing host command")
	}
	return rest[0], rest[1:], nil
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
