package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"unknwon.dev/kargo-tui/internal/auth"
	"unknwon.dev/kargo-tui/internal/config"
)

// resolveContext returns the Kargo context the TUI should run against.
//
// Resolution order:
//  1. --context flag (errors if missing).
//  2. CurrentContext from config.
//  3. The single configured context, if exactly one exists.
//  4. Interactive picker if multiple contexts exist.
//  5. Inline SSO login flow if no context is configured.
func resolveContext(ctx context.Context, cfg *config.Config, override string) (*config.Context, error) {
	if override != "" {
		c := cfg.Find(override)
		if c == nil {
			return nil, fmt.Errorf("context %q not found; configured: %s",
				override, contextNames(cfg))
		}
		return c, nil
	}
	if cfg.CurrentContext != "" {
		if c := cfg.Find(cfg.CurrentContext); c != nil {
			return c, nil
		}
	}
	switch len(cfg.Contexts) {
	case 0:
		return promptFirstLogin(ctx, cfg)
	case 1:
		return cfg.Contexts[0], nil
	default:
		return pickContext(cfg)
	}
}

// promptFirstLogin runs an interactive SSO login on first use and saves the
// resulting context. Used when the config file has zero contexts so the user
// doesn't have to quit and re-run a separate `auth login` command.
func promptFirstLogin(ctx context.Context, _ *config.Config) (*config.Context, error) {
	fmt.Fprintln(os.Stderr, "No Kargo context configured.")
	fmt.Fprintln(os.Stderr, "Tip: pass --context next time, or run `kargo-tui auth login <url>`.")
	fmt.Fprint(os.Stderr, "Kargo API URL: ")

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read URL: %w", err)
	}
	apiURL := strings.TrimSpace(line)
	if apiURL == "" {
		return nil, errors.New("no URL provided")
	}

	saved, err := auth.SSOLogin(ctx, auth.LoginOptions{
		APIAddress:  apiURL,
		MakeCurrent: true,
	}, nil)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "Saved context %q.\n", saved.Name)
	return saved, nil
}

// pickContext prints a numbered list of configured contexts and reads the
// user's choice from stdin. Selecting a context also persists it as the new
// CurrentContext so the next launch is non-interactive.
func pickContext(cfg *config.Config) (*config.Context, error) {
	fmt.Fprintln(os.Stderr, "Select a Kargo context:")
	for i, c := range cfg.Contexts {
		marker := "  "
		if c.Name == cfg.CurrentContext {
			marker = "* "
		}
		fmt.Fprintf(os.Stderr, "  %s%d) %s\t%s\n", marker, i+1, c.Name, c.APIAddress)
	}
	fmt.Fprintln(os.Stderr, "Tip: --context <name> skips this prompt; `kargo-tui auth login <url>` adds another.")
	fmt.Fprint(os.Stderr, "Choice (number or name): ")

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read choice: %w", err)
	}
	choice := strings.TrimSpace(line)
	if choice == "" {
		return nil, errors.New("no choice provided")
	}
	if n, err := strconv.Atoi(choice); err == nil && n >= 1 && n <= len(cfg.Contexts) {
		picked := cfg.Contexts[n-1]
		persistCurrent(cfg, picked.Name)
		return picked, nil
	}
	if c := cfg.Find(choice); c != nil {
		persistCurrent(cfg, c.Name)
		return c, nil
	}
	return nil, fmt.Errorf("unknown context %q", choice)
}

// persistCurrent updates CurrentContext and writes the config back. Failures
// are non-fatal — the run continues with the in-memory choice.
func persistCurrent(cfg *config.Config, name string) {
	cfg.CurrentContext = name
	if err := config.Save(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to persist current context: %v\n", err)
	}
}

func contextNames(cfg *config.Config) string {
	names := make([]string, 0, len(cfg.Contexts))
	for _, c := range cfg.Contexts {
		names = append(names, c.Name)
	}
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}
