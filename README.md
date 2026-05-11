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

## License

This project is under the MIT License. See the [LICENSE](LICENSE) file for the full license text.
