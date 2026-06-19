package main

import (
	"context"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/cockroachdb/errors"
	"github.com/urfave/cli/v3"

	"unknwon.dev/kargo-tui/internal/auth"
	"unknwon.dev/kargo-tui/internal/config"
	"unknwon.dev/kargo-tui/internal/kargo"
	"unknwon.dev/kargo-tui/internal/tracing"
	"unknwon.dev/kargo-tui/internal/tui"
)

// Populated at build time via -ldflags '-X main.buildVersion=...
// -X main.buildDate=... -X main.buildCommit=...'. buildVersion is the
// release tag (e.g. "v1.2.3") for tagged release builds, or
// "<closest-tag>+dev" for local builds via moon.
var (
	buildVersion = "dev"
	buildDate    = "unknown"
	buildCommit  = "unknown"
)

func main() {
	tui.SetBuildInfo(buildVersion, buildCommit, buildDate)

	cmd := &cli.Command{
		Name:    "kargo-tui",
		Usage:   "Interactive TUI for the Kargo continuous-delivery API",
		Version: fmt.Sprintf("%s (commit %s, built %s)", buildVersion, buildCommit, buildDate),
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
	// Tracing is opt-in via KARGO_TUI_TRACE_FILE; unset → noop.
	traceShutdown, err := tracing.Init("kargo-tui", buildVersion)
	if err != nil {
		return errors.Wrap(err, "init tracing")
	}
	defer func() {
		// Use a fresh context for shutdown so a cancelled parent doesn't
		// drop in-flight spans before the batched exporter can flush.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = traceShutdown(shutdownCtx)
	}()

	cfg, err := config.Load()
	if err != nil {
		return errors.Wrap(err, "load config")
	}
	active, err := resolveContext(ctx, cfg, cmd.String("context"))
	if err != nil {
		return errors.Wrap(err, "resolve context")
	}

	client, err := kargo.NewClient(active)
	if err != nil {
		return errors.Wrap(err, "create Kargo client")
	}
	attachRefresher(client, active)
	primeToken(client, active)
	project := cmd.String("project")
	if project == "" {
		project = active.Project
	}

	// If no project was selected, try to auto-select when there's exactly one;
	// otherwise fall through to the picker. Auth failures here are non-fatal:
	// the TUI launches with the banner up so the user can press R to recover.
	authExpired := false
	if project == "" {
		ps, err := client.ListProjects(ctx)
		switch {
		case err == nil && len(ps) == 1:
			project = ps[0]
		case kargo.IsUnauthenticated(err):
			authExpired = true
		}
	}

	ctxNames, ctxBuilder, ctxLogin, ctxRelogin, ctxCanRelogin, ctxDelete, ctxPersistProject := contextSwitcher(cfg)

	// Detect the terminal's background brightness while we're still in
	// cooked mode. Doing this after bubbletea takes over would just
	// echo back the bg color the renderer itself set.
	termDark, termDetected := detectTerminalBackgroundDark()

	// Preload the Argo shard table synchronously so the panel can
	// render links from the first frame. Failure is non-fatal: the
	// model just gets a nil map and falls back to no-link rendering.
	shards, _ := client.DiscoverArgoCDShards(ctx)

	var p *tea.Program
	if project == "" {
		m := tui.NewWithPicker(client, active.Name).
			WithArgoShards(active.Name, shards).
			WithContexts(ctxNames, ctxBuilder, ctxLogin, ctxRelogin, ctxCanRelogin, ctxDelete, ctxPersistProject)
		if termDetected {
			m = m.WithDetectedDark(termDark)
		}
		if authExpired {
			m = m.WithAuthExpired("saved session expired")
		}
		p = tea.NewProgram(m)
	} else {
		client.SetProject(project)
		// ListStages / ListFreight failures are non-fatal here too. If the
		// cause is auth, the TUI takes over with the banner. If it's a real
		// server problem, the per-view error line surfaces it once inside.
		deploys, dErr := client.ListStages(ctx, project)
		freights, fErr := client.ListFreight(ctx, project)
		if kargo.IsUnauthenticated(dErr) || kargo.IsUnauthenticated(fErr) {
			authExpired = true
		}
		m := tui.New(client, active.Name, project, deploys, freights).
			WithArgoShards(active.Name, shards).
			WithContexts(ctxNames, ctxBuilder, ctxLogin, ctxRelogin, ctxCanRelogin, ctxDelete, ctxPersistProject)
		if termDetected {
			m = m.WithDetectedDark(termDark)
		}
		if authExpired {
			m = m.WithAuthExpired("saved session expired")
		}
		p = tea.NewProgram(m)
	}

	// Inject the program's thread-safe Send so the SSO login goroutine can
	// stream status updates back into the TUI.
	go p.Send(tui.SetSendMsg{Send: p.Send})

	if _, err := p.Run(); err != nil {
		return errors.Wrap(err, "run TUI")
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

// primeToken refreshes the bearer synchronously so the synchronous
// startup RPCs (ListProjects, ListStages, ListFreight, GetConfig for
// argo shards) see a valid token on the first try instead of relying
// on the lazy 401 retry. No-op when the context has no refresh token
// (admin-token logins); failures are non-fatal because the lazy retry
// path still recovers if there's anything to recover from.
func primeToken(client *kargo.Client, active *config.Context) {
	if active == nil || active.RefreshToken == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = client.ForceRefresh(ctx)
}

// contextSwitcher returns the list of configured context names, a builder
// that constructs a fresh client + that context's default project for a
// chosen name, a login callback that runs the SSO flow against a new
// Kargo URL and saves it as a new context, and a relogin callback that
// re-runs SSO against an *existing* context, preserving its
// insecureSkipTLSVerify and project flags. It also returns a canRelogin
// predicate (true only when a context has a refresh token, so admin-token
// contexts don't trigger SSO), a delete callback that removes a context,
// and a persistProject callback that records the active project on its
// context so the next cold start reopens it. The builder also persists the
// chosen context as CurrentContext so the next launch is non-interactive;
// failures from Save are non-fatal — the in-memory switch still completes.
func contextSwitcher(cfg *config.Config) (
	[]string,
	func(string) (*kargo.Client, string, error),
	func(ctx context.Context, url string, status func(string)) (string, error),
	func(ctx context.Context, contextName string, status func(string)) (string, error),
	func(name string) bool,
	func(name string) error,
	func(contextName, project string),
) {
	names := make([]string, 0, len(cfg.Contexts))
	for _, c := range cfg.Contexts {
		names = append(names, c.Name)
	}
	build := func(name string) (*kargo.Client, string, error) {
		c := cfg.Find(name)
		if c == nil {
			return nil, "", errors.Newf("context %q not found", name)
		}
		client, err := kargo.NewClient(c)
		if err != nil {
			return nil, "", errors.Wrap(err, "create Kargo client")
		}
		attachRefresher(client, c)
		// Mirror the startup priming so the first burst of RPCs after a
		// context switch (deploys, freights, argo shard discovery) doesn't
		// race the lazy 401 retry path. Without this, the one-shot
		// GetConfig that powers Argo links can land on a stale token and
		// fail, leaving the shard table empty for the rest of the session.
		primeToken(client, c)
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
			return "", errors.Wrap(err, "log in to context")
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
			return "", errors.Newf("context %q not found", name)
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
			return "", errors.Wrap(err, "re-login to context")
		}
		// Reload again so the in-memory cfg reflects the rotated tokens.
		if fresh, err := config.Load(); err == nil {
			*cfg = *fresh
		}
		return saved.Name, nil
	}
	canRelogin := func(name string) bool {
		// Read fresh from disk so a concurrent `kargo-tui auth login` that
		// just minted a refresh token is honoured. SSO recovery is only
		// possible when the context actually has one; admin-token contexts
		// don't and must not trigger the SSO flow.
		fresh, err := config.Load()
		if err == nil {
			*cfg = *fresh
		}
		c := cfg.Find(name)
		return c != nil && c.RefreshToken != ""
	}
	del := func(name string) error {
		// Read fresh from disk so a concurrent CLI edit isn't clobbered,
		// then remove and persist. Mirror the removal into the in-memory
		// cfg the builder/login closures share.
		fresh, err := config.Load()
		if err != nil {
			return errors.Wrap(err, "load config")
		}
		if !fresh.Remove(name) {
			return errors.Newf("context %q not found", name)
		}
		if err := config.Save(fresh); err != nil {
			return errors.Wrap(err, "save config")
		}
		*cfg = *fresh
		return nil
	}
	persistProject := func(contextName, project string) {
		if contextName == "" || project == "" {
			return
		}
		// Read fresh from disk so a concurrent CLI edit isn't clobbered,
		// then record the active project so the next cold start reopens it.
		// Best-effort: a save failure shouldn't disrupt the running TUI.
		fresh, err := config.Load()
		if err != nil {
			return
		}
		c := fresh.Find(contextName)
		if c == nil || c.Project == project {
			return
		}
		c.Project = project
		if err := config.Save(fresh); err != nil {
			return
		}
		*cfg = *fresh
	}
	return names, build, login, relogin, canRelogin, del, persistProject
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
						return errors.New("usage: kargo-tui auth login <url>")
					}
					saved, err := auth.SSOLogin(ctx, auth.LoginOptions{
						APIAddress:            cmd.Args().First(),
						ContextName:           cmd.String("name"),
						Project:               cmd.String("project"),
						CallbackPort:          cmd.Int("callback-port"),
						InsecureSkipTLSVerify: cmd.Bool("insecure-skip-tls-verify"),
						MakeCurrent:           !cmd.Bool("no-make-current"),
					}, nil)
					if err != nil {
						return errors.Wrap(err, "log in")
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
						return errors.New("usage: kargo-tui auth logout <context-name>")
					}
					cfg, err := config.Load()
					if err != nil {
						return errors.Wrap(err, "load config")
					}
					if !cfg.Remove(name) {
						return errors.Newf("context %q not found", name)
					}
					if err := config.Save(cfg); err != nil {
						return errors.Wrap(err, "save config")
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
						return errors.Wrap(err, "load config")
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
						return errors.New("usage: kargo-tui contexts use <context-name>")
					}
					cfg, err := config.Load()
					if err != nil {
						return errors.Wrap(err, "load config")
					}
					if cfg.Find(name) == nil {
						return errors.Newf("context %q not found", name)
					}
					cfg.CurrentContext = name
					if err := config.Save(cfg); err != nil {
						return errors.Wrap(err, "save config")
					}
					fmt.Printf("switched to context %q\n", name)
					return nil
				},
			},
		},
	}
}
