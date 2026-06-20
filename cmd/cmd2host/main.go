// Command cmd2host runs the host-side proxy daemon and provides config
// management subcommands. It is a thin wrapper over the importable packages
// under pkg/.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/taisukeoe/cmd2host/pkg/auth"
	"github.com/taisukeoe/cmd2host/pkg/config"
	"github.com/taisukeoe/cmd2host/pkg/daemon"
)

// version is set at build time via -ldflags "-X main.version=..."
var version = "dev"

func main() {
	// Handle --version flag
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("cmd2host version %s\n", version)
		return
	}

	// Handle --hash-token for generating token hashes (used by init scripts)
	// Token is read from stdin to avoid exposure in process list (ps aux)
	if len(os.Args) == 2 && os.Args[1] == "--hash-token" {
		token, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading token from stdin: %v\n", err)
			os.Exit(1)
		}
		tokenStr := strings.TrimSpace(string(token))
		if tokenStr == "" {
			fmt.Fprintln(os.Stderr, "Error: empty token")
			os.Exit(1)
		}
		fmt.Println(auth.HashToken(tokenStr))
		return
	}

	// Handle config subcommands
	if len(os.Args) >= 2 && os.Args[1] == "config" {
		handleConfigCommand()
		return
	}

	// Handle suggest-submodules subcommand (config-related but takes a path arg)
	if len(os.Args) >= 2 && os.Args[1] == "suggest-submodules" {
		handleSuggestSubmodulesCommand()
		return
	}

	// Handle projects subcommand
	if len(os.Args) == 2 && os.Args[1] == "projects" {
		handleProjectsCommand()
		return
	}

	// Handle templates subcommand
	if len(os.Args) >= 2 && os.Args[1] == "templates" {
		handleTemplatesCommand()
		return
	}

	// Default: run daemon
	runDaemon()
}

// handleConfigCommand handles config subcommands
func handleConfigCommand() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: cmd2host config <command> [args]")
		fmt.Fprintln(os.Stderr, "Commands:")
		fmt.Fprintln(os.Stderr, "  init --repo=<owner/repo> --repo-path=<path> [more --repo/--repo-path pairs] [options]  Create project config from template")
		fmt.Fprintln(os.Stderr, "  diff <project-id>                                                                      Show config diff and current hash")
		fmt.Fprintln(os.Stderr, "  allow <project-id>                                                                     Allow current config")
		fmt.Fprintln(os.Stderr, "  migrate <project-id>                                                                   Migrate a legacy 1:1 config to the new repos/repo_paths form")
		os.Exit(1)
	}

	subCmd := os.Args[2]

	switch subCmd {
	case "init":
		handleConfigInit()

	case "diff":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: cmd2host config diff <project-id>")
			os.Exit(1)
		}
		projectID := os.Args[3]
		handleConfigDiff(projectID)

	case "allow":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: cmd2host config allow <project-id>")
			os.Exit(1)
		}
		projectID := os.Args[3]
		handleConfigAllow(projectID)

	case "migrate":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: cmd2host config migrate <project-id> [--apply]")
			os.Exit(1)
		}
		projectID := os.Args[3]
		apply := false
		for i := 4; i < len(os.Args); i++ {
			switch os.Args[i] {
			case "--apply":
				apply = true
			default:
				fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", os.Args[i])
				os.Exit(1)
			}
		}
		handleConfigMigrate(projectID, apply)

	default:
		fmt.Fprintf(os.Stderr, "Unknown config command: %s\n", subCmd)
		os.Exit(1)
	}
}

// handleConfigInit creates a new project config from a template.
// --repo and --repo-path are both repeatable; they pair up in declaration
// order to populate the new repos / repo_paths arrays.
func handleConfigInit() {
	var opts config.CreateProjectConfigOptions
	showHelp := false

	for i := 3; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch {
		case arg == "--help" || arg == "-h":
			showHelp = true
		case strings.HasPrefix(arg, "--repo="):
			opts.Repos = append(opts.Repos, strings.TrimPrefix(arg, "--repo="))
		case strings.HasPrefix(arg, "--template="):
			opts.Template = strings.TrimPrefix(arg, "--template=")
		case strings.HasPrefix(arg, "--repo-path="):
			opts.RepoPaths = append(opts.RepoPaths, strings.TrimPrefix(arg, "--repo-path="))
		case arg == "--allow":
			opts.Allow = true
		case arg == "--force":
			opts.Force = true
		default:
			fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", arg)
			os.Exit(1)
		}
	}

	if showHelp || len(opts.Repos) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: cmd2host config init --repo=<owner/repo> --repo-path=<path> [more --repo / --repo-path pairs] [options]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Options:")
		fmt.Fprintln(os.Stderr, "  --repo=<owner/repo>   Repository name (required, repeatable)")
		fmt.Fprintln(os.Stderr, "  --repo-path=<path>    Local repository path (repeatable, must match --repo count)")
		fmt.Fprintln(os.Stderr, "  --template=<name>     Template name (default: readonly)")
		fmt.Fprintln(os.Stderr, "  --allow               Allow config after creation")
		fmt.Fprintln(os.Stderr, "  --force               Overwrite existing config")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "The first --repo / --repo-path pair is the project's primary (parent) entry;")
		fmt.Fprintln(os.Stderr, "subsequent pairs are submodules or sibling repos hosted under the parent workspace.")
		fmt.Fprintln(os.Stderr, "The project ID is derived from the first --repo.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Available templates:")
		templates, err := config.ListTemplates()
		if err != nil {
			fmt.Fprintf(os.Stderr, "  (error listing templates: %v)\n", err)
		} else {
			for _, t := range templates {
				fmt.Fprintf(os.Stderr, "  - %s\n", t)
			}
		}
		if len(opts.Repos) == 0 {
			os.Exit(1)
		}
		return
	}

	if err := config.CreateProjectConfig(opts); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	projectID := config.NormalizeProjectID(opts.Repos[0])
	configPath := config.ProjectConfigPath(projectID)
	fmt.Printf("Created config: %s\n", configPath)
	if opts.Allow {
		fmt.Println("Config allowed.")
	} else {
		fmt.Printf("\nTo allow, run: cmd2host config allow %s\n", projectID)
	}

	fmt.Println("\nDevContainer setup:")
	fmt.Println("  1. Copy host/scripts/init-cmd2host.sh to .devcontainer/")
	fmt.Println("  2. Add to devcontainer.json:")
	fmt.Println("     - initializeCommand: \".devcontainer/init-cmd2host.sh\"")
	fmt.Println("     - Mount session token to /run/cmd2host-token")
	fmt.Println("")
	fmt.Println("Connection modes:")
	fmt.Println("  TCP (default): connectionMode: \"tcp\" - uses host.docker.internal:9876")
	fmt.Println("  Unix socket:   connectionMode: \"unix\" - mount ~/.cmd2host/cmd2host.sock")
	fmt.Println("                 Required for --network none containers")
}

// handleConfigMigrate normalizes a legacy 1:1 config (repo / repo_path)
// to the new 1:N form (repos / repo_paths). Shows a dry-run diff and only
// writes when --apply is given. Does NOT re-stamp allowed.sha256 — the user
// must run `cmd2host config allow <project-id>` after a successful migrate.
func handleConfigMigrate(projectID string, apply bool) {
	configPath := config.ProjectConfigPath(projectID)
	original, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading config: %v\n", err)
		os.Exit(1)
	}

	cfg, err := config.LoadProjectConfig(projectID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	canonical, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error re-encoding config: %v\n", err)
		os.Exit(1)
	}
	canonical = append(canonical, '\n')

	if string(original) == string(canonical) {
		fmt.Println("No migration needed: config already in canonical form.")
		return
	}

	fmt.Println("Migration preview (legacy → canonical):")
	fmt.Println("--- before ---")
	fmt.Println(strings.TrimRight(string(original), "\n"))
	fmt.Println("--- after ----")
	fmt.Println(strings.TrimRight(string(canonical), "\n"))
	fmt.Println("--- end ------")

	if !apply {
		fmt.Println("\nDry-run only. Pass --apply to rewrite the config file.")
		fmt.Println("After applying, run: cmd2host config allow " + projectID)
		return
	}

	if err := atomicWriteFile(configPath, canonical, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Migrated: %s\n", configPath)
	fmt.Printf("Run: cmd2host config allow %s\n", projectID)
}

// atomicWriteFile writes data to a temp file in the same directory as path,
// then renames it onto path. A crash or disk-full mid-write leaves the
// original file intact rather than truncated.
func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("failed to chmod temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("failed to fsync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("failed to rename temp file onto %s: %w", path, err)
	}
	return nil
}

// handleSuggestSubmodulesCommand parses .gitmodules at the given repo path
// (default: cwd) and prints suggested --repo / --repo-path pairs that the
// user can copy into a cmd2host config init invocation. It does NOT modify
// any project config — auto-allow is intentionally avoided to prevent
// vendored third-party submodules from leaking into the allow list.
func handleSuggestSubmodulesCommand() {
	repoRoot := "."
	for i := 2; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(os.Stderr, "Usage: cmd2host suggest-submodules [--repo-root=<path>]")
			os.Exit(0)
		case strings.HasPrefix(arg, "--repo-root="):
			repoRoot = strings.TrimPrefix(arg, "--repo-root=")
		default:
			fmt.Fprintf(os.Stderr, "Unknown flag: %s\n", arg)
			os.Exit(1)
		}
	}

	suggestions, err := config.SuggestSubmodules(repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(suggestions) == 0 {
		fmt.Println("No submodules found (no .gitmodules entries with parseable owner/repo).")
		return
	}

	fmt.Println("Submodule suggestions (review before adding to your project config):")
	for _, s := range suggestions {
		fmt.Printf("  --repo=%s --repo-path=%s\n", s.Repo, s.RepoPath)
	}
	fmt.Println("\nNote: these are suggestions only. Vendored / third-party submodules should not")
	fmt.Println("be added to the project allow list. Review each entry before copying it into")
	fmt.Println("cmd2host config init or .devcontainer/cmd2host.repos.")
}

// handleConfigDiff shows config status and hash
func handleConfigDiff(projectID string) {
	configPath := config.ProjectConfigPath(projectID)
	allowedPath := config.AllowedHashPath(projectID)

	// Check if config exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Config not found: %s\n", configPath)
		os.Exit(1)
	}

	// Compute current hash
	currentHash, err := config.ComputeConfigHash(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error computing hash: %v\n", err)
		os.Exit(1)
	}

	// Read allowed hash
	var allowedHash string
	allowedData, err := os.ReadFile(allowedPath)
	if err == nil {
		allowedHash = strings.TrimSpace(string(allowedData))
	}

	fmt.Printf("Project:       %s\n", projectID)
	fmt.Printf("Config:        %s\n", configPath)
	fmt.Printf("Current hash:  %s\n", currentHash)

	if allowedHash == "" {
		fmt.Printf("Allowed hash:  (none)\n")
		fmt.Println("\nStatus: NOT ALLOWED")
		fmt.Printf("\nTo allow, run: cmd2host config allow %s\n", projectID)
	} else if currentHash == allowedHash {
		fmt.Printf("Allowed hash:  %s\n", allowedHash)
		fmt.Println("\nStatus: ALLOWED (hashes match)")
	} else {
		fmt.Printf("Allowed hash:  %s\n", allowedHash)
		fmt.Println("\nStatus: MODIFIED (hashes differ)")
		fmt.Printf("\nTo allow changes, run: cmd2host config allow %s\n", projectID)
	}
}

// handleConfigAllow allows the current config
func handleConfigAllow(projectID string) {
	configPath := config.ProjectConfigPath(projectID)

	// Check if config exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Config not found: %s\n", configPath)
		os.Exit(1)
	}

	// Validate config first
	_, err := config.LoadProjectConfig(projectID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid config: %v\n", err)
		os.Exit(1)
	}

	// Allow
	if err := config.AllowConfig(projectID); err != nil {
		fmt.Fprintf(os.Stderr, "Error allowing config: %v\n", err)
		os.Exit(1)
	}

	hash, _ := config.ComputeConfigHash(configPath)
	fmt.Printf("Allowed config for project: %s\n", projectID)
	fmt.Printf("Hash: %s\n", hash)
}

// handleProjectsCommand lists all configured projects
func handleProjectsCommand() {
	projects, err := config.ListProjects()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing projects: %v\n", err)
		os.Exit(1)
	}

	if len(projects) == 0 {
		fmt.Println("No projects configured.")
		fmt.Printf("Project configs are stored in: %s\n", config.ProjectsDir())
		return
	}

	fmt.Println("Configured projects:")
	for _, p := range projects {
		allowed, _, err := config.IsConfigAllowed(p)
		status := "allowed"
		if err != nil || !allowed {
			status = "not allowed"
		}
		fmt.Printf("  %s (%s)\n", p, status)
	}
}

// handleTemplatesCommand handles templates subcommands
func handleTemplatesCommand() {
	// cmd2host templates - list templates
	// cmd2host templates show <name> - show template content
	if len(os.Args) == 2 {
		// List templates
		templates, err := config.ListTemplates()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing templates: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("Available templates:")
		for _, t := range templates {
			fmt.Printf("  %s\n", t)
		}
		return
	}

	subCmd := os.Args[2]

	switch subCmd {
	case "show":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: cmd2host templates show <name>")
			os.Exit(1)
		}
		name := os.Args[3]
		data, err := config.GetTemplate(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(data))

	default:
		fmt.Fprintf(os.Stderr, "Unknown templates command: %s\n", subCmd)
		fmt.Fprintln(os.Stderr, "Usage: cmd2host templates [show <name>]")
		os.Exit(1)
	}
}

// resolveDaemonConfigPath returns the daemon.json path to load on startup.
//
// Priority (most specific first):
//  1. DAEMON_CONFIG (single-file path override; used by tests and ad-hoc runs)
//  2. CMD2HOST_CONFIG_DIR/daemon.json (per-session base dir override; via
//     DefaultDaemonConfigPath → the cmd2host base dir resolution)
//  3. $HOME/.cmd2host/daemon.json (legacy default)
func resolveDaemonConfigPath() string {
	if path := os.Getenv("DAEMON_CONFIG"); path != "" {
		return path
	}
	return config.DefaultDaemonConfigPath()
}

// runDaemon starts the daemon server
func runDaemon() {
	configPath := resolveDaemonConfigPath()

	daemonConfig, err := config.LoadDaemonConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Daemon config error: %v\n", err)
		os.Exit(1)
	}
	for _, w := range daemonConfig.Warnings {
		fmt.Fprintf(os.Stderr, "cmd2host: %s\n", w)
	}

	server, err := daemon.NewServer(daemonConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Server initialization error: %v\n", err)
		os.Exit(1)
	}

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		fmt.Println("\nShutting down...")
		server.Shutdown()
	}()

	if err := server.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
