// Package config persists kargo-tui's CLI state — the list of configured
// Kargo instances ("contexts"), credentials for each, and which one is
// currently active. The file lives at $XDG_CONFIG_HOME/kargo-tui/config.yaml
// (default ~/.config/kargo-tui/config.yaml on macOS/Linux).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the on-disk state. Multiple Kargo servers can be registered as
// named Contexts; CurrentContext picks the active one.
type Config struct {
	CurrentContext string     `yaml:"currentContext,omitempty"`
	Contexts       []*Context `yaml:"contexts,omitempty"`
}

// Context holds everything needed to talk to one Kargo server.
type Context struct {
	Name                  string `yaml:"name"`
	APIAddress            string `yaml:"apiAddress"`
	BearerToken           string `yaml:"bearerToken,omitempty"`
	RefreshToken          string `yaml:"refreshToken,omitempty"`
	InsecureSkipTLSVerify bool   `yaml:"insecureSkipTLSVerify,omitempty"`
	Project               string `yaml:"project,omitempty"`
}

// Path returns the absolute path of the config file. It honors
// $KARGO_TUI_CONFIG when set, otherwise falls back to
// $XDG_CONFIG_HOME/kargo-tui/config.yaml or ~/.config/kargo-tui/config.yaml.
func Path() (string, error) {
	p := os.Getenv("KARGO_TUI_CONFIG")
	if p != "" {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", fmt.Errorf("resolve config path %s: %w", p, err)
		}
		return abs, nil
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate home dir: %w", err)
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "kargo-tui", "config.yaml"), nil
}

// Load reads the config file. A missing file is not an error — it returns an
// empty Config so first-run flows can write to it.
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return &c, nil
}

// Save atomically writes the config file with 0600 permissions (it contains
// bearer tokens). Parent directories are created on demand.
func Save(c *Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".config.*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, p); err != nil {
		return fmt.Errorf("rename to %s: %w", p, err)
	}
	return nil
}

// Find returns the named context, or nil if not present.
func (c *Config) Find(name string) *Context {
	for _, ctx := range c.Contexts {
		if ctx.Name == name {
			return ctx
		}
	}
	return nil
}

// Active returns the currently selected context. If a name is provided it
// overrides CurrentContext. Returns an error if the chosen context is missing
// or no contexts are configured.
func (c *Config) Active(override string) (*Context, error) {
	name := override
	if name == "" {
		name = c.CurrentContext
	}
	if name == "" {
		if len(c.Contexts) == 1 {
			return c.Contexts[0], nil
		}
		if len(c.Contexts) == 0 {
			return nil, fmt.Errorf("no Kargo contexts configured; run `kargo-tui auth login <url>`")
		}
		return nil, fmt.Errorf("no current context set; pass --context or run `kargo-tui contexts use <name>`")
	}
	if ctx := c.Find(name); ctx != nil {
		return ctx, nil
	}
	return nil, fmt.Errorf("context %q not found", name)
}

// Upsert replaces an existing context with the same name or appends a new one.
func (c *Config) Upsert(ctx *Context) {
	for i, existing := range c.Contexts {
		if existing.Name == ctx.Name {
			c.Contexts[i] = ctx
			return
		}
	}
	c.Contexts = append(c.Contexts, ctx)
}

// Remove deletes the named context. Returns true if it existed.
func (c *Config) Remove(name string) bool {
	for i, ctx := range c.Contexts {
		if ctx.Name == name {
			c.Contexts = append(c.Contexts[:i], c.Contexts[i+1:]...)
			if c.CurrentContext == name {
				c.CurrentContext = ""
			}
			return true
		}
	}
	return false
}
