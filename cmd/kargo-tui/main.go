package main

import (
	"context"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/urfave/cli/v3"

	"unknwon.dev/kargo-tui/internal/auth"
	"unknwon.dev/kargo-tui/internal/config"
	"unknwon.dev/kargo-tui/internal/kargo"
	"unknwon.dev/kargo-tui/internal/tui"
)

func main() {
	cmd := &cli.Command{
		Name:  "kargo-tui",
		Usage: "Interactive TUI for the Kargo continuous-delivery API",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "context",
				Usage:   "Configured Kargo context to use (default: current context)",
				Sources: cli.EnvVars("KARGO_TUI_CONTEXT"),
			},
			&cli.StringFlag{
				Name:    "project",
				Aliases: []string{"p"},
				Usage:   "Kargo project to open. Leave empty to pick interactively.",
				Sources: cli.EnvVars("KARGO_PROJECT"),
			},
		},
		Action:   runTUI,
		Commands: []*cli.Command{authCommand(), contextsCommand()},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// runTUI is the root command's action: open the TUI against the active
// Kargo context. If no context is configured, prompt to log in. If multiple
// are configured and none is selected, prompt the user to pick one.
func runTUI(ctx context.Context, cmd *cli.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	active, err := resolveContext(ctx, cfg, cmd.String("context"))
	if err != nil {
		return err
	}

	client, err := kargo.NewClient(active)
	if err != nil {
		return err
	}
	attachRefresher(client, active)
	project := cmd.String("project")
	if project == "" {
		project = active.Project
	}

	// If no project was selected, try to auto-select when there's exactly one;
	// otherwise fall through to the picker.
	if project == "" {
		ps, err := client.ListProjects(ctx)
		if err == nil && len(ps) == 1 {
			project = ps[0]
		}
	}

	ctxNames, ctxBuilder, ctxLogin, ctxRelogin := contextSwitcher(cfg)

	var p *tea.Program
	if project == "" {
		p = tea.NewProgram(tui.NewWithPicker(client, active.Name).
			WithContexts(ctxNames, ctxBuilder, ctxLogin, ctxRelogin))
	} else {
		client.SetProject(project)
		deploys, err := client.ListStages(ctx, project)
		if err != nil {
			return fmt.Errorf("load deploys: %w", err)
		}
		freights, err := client.ListFreight(ctx, project)
		if err != nil {
			return fmt.Errorf("load freights: %w", err)
		}
		p = tea.NewProgram(tui.New(client, active.Name, project, deploys, freights).
			WithContexts(ctxNames, ctxBuilder, ctxLogin, ctxRelogin))
	}

	// Inject the program's thread-safe Send so the SSO login goroutine can
	// stream status updates back into the TUI.
	go p.Send(tui.SetSendMsg{Send: p.Send})

	if _, err := p.Run(); err != nil {
		return err
	}
	return nil
}

// attachRefresher wires an OIDC refresh-token exchange into the client's
// transport. When a Kargo RPC fails with CodeUnauthenticated the transport
// invokes the refresher, which swaps the saved refresh_token for a fresh
// id_token, persists both back to the config, and the call is retried
// once. No-op when the context has no refresh_token (admin-token logins).
func attachRefresher(client *kargo.Client, c *config.Context) {
	if c == nil || c.RefreshToken == "" {
		return
	}
	r := auth.NewRefresher(c.Name, c.InsecureSkipTLSVerify)
	client.SetTokenRefresher(r.Refresh)
}

// contextSwitcher returns the list of configured context names, a builder
// that constructs a fresh client + that context's default project for a
// chosen name, a login callback that runs the SSO flow against a new
// Kargo URL and saves it as a new context, and a relogin callback that
// re-runs SSO against an *existing* context, preserving its
// insecureSkipTLSVerify and project flags. The builder also persists the
// chosen context as CurrentContext so the next launch is non-interactive;
// failures from Save are non-fatal — the in-memory switch still completes.
func contextSwitcher(cfg *config.Config) (
	[]string,
	func(string) (*kargo.Client, string, error),
	func(ctx context.Context, url string, status func(string)) (string, error),
	func(ctx context.Context, contextName string, status func(string)) (string, error),
) {
	names := make([]string, 0, len(cfg.Contexts))
	for _, c := range cfg.Contexts {
		names = append(names, c.Name)
	}
	build := func(name string) (*kargo.Client, string, error) {
		c := cfg.Find(name)
		if c == nil {
			return nil, "", fmt.Errorf("context %q not found", name)
		}
		client, err := kargo.NewClient(c)
		if err != nil {
			return nil, "", err
		}
		attachRefresher(client, c)
		cfg.CurrentContext = name
		_ = config.Save(cfg)
		return client, c.Project, nil
	}
	login := func(ctx context.Context, url string, status func(string)) (string, error) {
		saved, err := auth.SSOLogin(ctx, auth.LoginOptions{
			APIAddress:  url,
			MakeCurrent: true,
			Quiet:       true, // TUI is in alt-screen; stderr writes corrupt the view
		}, status)
		if err != nil {
			return "", err
		}
		// Refresh the in-memory config from disk so subsequent build()
		// calls see the new context.
		fresh, err := config.Load()
		if err == nil {
			*cfg = *fresh
		}
		return saved.Name, nil
	}
	relogin := func(ctx context.Context, name string, status func(string)) (string, error) {
		// Read the saved context fresh from disk so flags set by a
		// concurrent `kargo-tui auth login --name ...` are honoured.
		fresh, err := config.Load()
		if err == nil {
			*cfg = *fresh
		}
		c := cfg.Find(name)
		if c == nil {
			return "", fmt.Errorf("context %q not found", name)
		}
		saved, err := auth.SSOLogin(ctx, auth.LoginOptions{
			APIAddress:            c.APIAddress,
			ContextName:           c.Name,
			Project:               c.Project,
			InsecureSkipTLSVerify: c.InsecureSkipTLSVerify,
			MakeCurrent:           true,
			Quiet:                 true,
		}, status)
		if err != nil {
			return "", err
		}
		// Reload again so the in-memory cfg reflects the rotated tokens.
		if fresh, err := config.Load(); err == nil {
			*cfg = *fresh
		}
		return saved.Name, nil
	}
	return names, build, login, relogin
}

// authCommand builds the `kargo-tui auth ...` subcommand tree.
func authCommand() *cli.Command {
	return &cli.Command{
		Name:  "auth",
		Usage: "Authenticate against a Kargo API server",
		Commands: []*cli.Command{
			{
				Name:      "login",
				Usage:     "Log in to a Kargo API server via SSO (OIDC)",
				ArgsUsage: "<url>",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "name",
						Usage: "Local name for this Kargo context (default: derived from URL host)",
					},
					&cli.StringFlag{
						Name:    "project",
						Aliases: []string{"p"},
						Usage:   "Default project for this context",
					},
					&cli.IntFlag{
						Name:  "callback-port",
						Usage: "Port to listen on for the OIDC callback (0 = pick a free port)",
					},
					&cli.BoolFlag{
						Name:  "insecure-skip-tls-verify",
						Usage: "Skip TLS certificate verification",
					},
					&cli.BoolFlag{
						Name:  "no-make-current",
						Usage: "Do not switch the active context to this one",
					},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					if cmd.NArg() < 1 {
						return fmt.Errorf("usage: kargo-tui auth login <url>")
					}
					saved, err := auth.SSOLogin(ctx, auth.LoginOptions{
						APIAddress:            cmd.Args().First(),
						ContextName:           cmd.String("name"),
						Project:               cmd.String("project"),
						CallbackPort:          int(cmd.Int("callback-port")),
						InsecureSkipTLSVerify: cmd.Bool("insecure-skip-tls-verify"),
						MakeCurrent:           !cmd.Bool("no-make-current"),
					}, nil)
					if err != nil {
						return err
					}
					fmt.Printf("logged in to %s as context %q\n", saved.APIAddress, saved.Name)
					return nil
				},
			},
			{
				Name:      "logout",
				Usage:     "Remove a stored Kargo context",
				ArgsUsage: "<context-name>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					name := cmd.Args().First()
					if name == "" {
						return fmt.Errorf("usage: kargo-tui auth logout <context-name>")
					}
					cfg, err := config.Load()
					if err != nil {
						return err
					}
					if !cfg.Remove(name) {
						return fmt.Errorf("context %q not found", name)
					}
					if err := config.Save(cfg); err != nil {
						return err
					}
					fmt.Printf("removed context %q\n", name)
					return nil
				},
			},
		},
	}
}

// contextsCommand builds the `kargo-tui contexts ...` subcommand tree for
// inspecting and switching between configured Kargo instances.
func contextsCommand() *cli.Command {
	return &cli.Command{
		Name:  "contexts",
		Usage: "Manage configured Kargo contexts",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List all configured Kargo contexts",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					cfg, err := config.Load()
					if err != nil {
						return err
					}
					if len(cfg.Contexts) == 0 {
						fmt.Println("no contexts configured; run `kargo-tui auth login <url>`")
						return nil
					}
					for _, c := range cfg.Contexts {
						marker := "  "
						if c.Name == cfg.CurrentContext {
							marker = "* "
						}
						fmt.Printf("%s%s\t%s\n", marker, c.Name, c.APIAddress)
					}
					return nil
				},
			},
			{
				Name:      "use",
				Usage:     "Switch the active Kargo context",
				ArgsUsage: "<context-name>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					name := cmd.Args().First()
					if name == "" {
						return fmt.Errorf("usage: kargo-tui contexts use <context-name>")
					}
					cfg, err := config.Load()
					if err != nil {
						return err
					}
					if cfg.Find(name) == nil {
						return fmt.Errorf("context %q not found", name)
					}
					cfg.CurrentContext = name
					if err := config.Save(cfg); err != nil {
						return err
					}
					fmt.Printf("switched to context %q\n", name)
					return nil
				},
			},
		},
	}
}
