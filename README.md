![kargo-tui](assets/banner.png)

Kargo is nice, Karog is cute, Kargo's UI works like crap,  Kargo's CLI won't even start. Don't ask me to reproduce, don't ask me to report. I got no energy talking to void.

Here it is, the missing Kargo console for operating with hundreds of stages. Entirely vibe-coded, use at your own risk.

## Local development

This project uses [moon](https://moonrepo.dev/) as its task runner. Install it with Homebrew:

```zsh
brew install moon
```

Common tasks:

```zsh
moon run :install   # Build and install the kargo-tui binary to $GOBIN.
moon run :lint      # Tidy Go modules and run golangci-lint.
moon run :test      # Run the test suite.
moon run :build     # Compile all packages.
```

After `moon run :install`, launch the TUI directly with `kargo-tui`.

## Authentication

`kargo-tui` authenticates against a Kargo API server using the same OIDC SSO (PKCE auth-code) flow as `kargo login --sso`. Credentials are saved as named "contexts" in the kargo-tui config file.

### First-run prompt

If no context is configured, launching `kargo-tui` prints `Kargo API URL:` and reads the URL from stdin, then opens your browser to complete SSO. The resulting context is saved and set as current, so subsequent launches are non-interactive.

You can also add a new context from inside the TUI via the context switcher, which runs the same SSO flow.

### Logging in manually

To log in (or add another context) without going through the first-run prompt:

```zsh
kargo-tui auth login <url>
```

Useful flags:

- `--name <ctx-name>` — local name for this context (default: derived from the URL host)
- `-p, --project <project>` — default project for this context
- `--callback-port <port>` — port for the OIDC callback listener (`0` picks a free port)
- `--insecure-skip-tls-verify` — skip TLS verification against the Kargo server
- `--no-make-current` — don't switch the active context to the one being created

Example:

```zsh
kargo-tui auth login https://kargo.example.com --name prod -p my-project
```

### Managing contexts

```zsh
kargo-tui contexts list           # show all configured contexts (* marks current)
kargo-tui contexts use <name>     # switch active context
kargo-tui auth logout <name>      # remove a stored context
kargo-tui --context <name>        # one-off override for a single launch
```

## License

This project is under the MIT License. See the [LICENSE](LICENSE) file for the full license text.
