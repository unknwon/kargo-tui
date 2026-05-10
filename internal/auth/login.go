// Package auth implements interactive login flows against a Kargo API server
// and writes the resulting credentials to kargo-tui's config file.
package auth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/akuity/kargo/pkg/client/generated/system"

	"unknwon.dev/kargo-tui/internal/config"
	"unknwon.dev/kargo-tui/internal/kargo"
)

// LoginOptions captures the inputs to an admin-password login flow.
type LoginOptions struct {
	APIAddress            string
	ContextName           string // defaults to host of APIAddress
	Project               string
	Password              string // if empty, prompted on stdin
	InsecureSkipTLSVerify bool
	MakeCurrent           bool
}

// AdminLogin performs a password-based admin login against the Kargo server,
// then persists the resulting bearer token under a named context in the
// kargo-tui config file. Returns the saved context for use by callers.
//
// The flow mirrors `kargo login --admin`: GetPublicConfig confirms the
// server has admin login enabled, then AdminLogin is called with the
// password placed in the Authorization header (the same wire-level trick the
// upstream CLI uses — see pkg/cli/cmd/login/login.go).
func AdminLogin(ctx context.Context, opts LoginOptions) (*config.Context, error) {
	if opts.APIAddress == "" {
		return nil, errors.New("API address is required")
	}
	if !strings.HasPrefix(opts.APIAddress, "http://") && !strings.HasPrefix(opts.APIAddress, "https://") {
		opts.APIAddress = "https://" + opts.APIAddress
	}

	if opts.ContextName == "" {
		opts.ContextName = defaultContextName(opts.APIAddress)
	}

	if opts.Password == "" {
		pw, err := readPassword(os.Stdin, fmt.Sprintf("Password for %s: ", opts.APIAddress))
		if err != nil {
			return nil, fmt.Errorf("read password: %w", err)
		}
		opts.Password = pw
	}

	publicAPI, err := kargo.NewUnauthenticatedAPI(opts.APIAddress, opts.InsecureSkipTLSVerify)
	if err != nil {
		return nil, err
	}
	pubResp, err := publicAPI.System.GetPublicConfig(
		system.NewGetPublicConfigParams().WithContext(ctx),
	)
	if err != nil {
		return nil, fmt.Errorf("retrieve public config: %w", err)
	}
	if pubResp.Payload == nil || !pubResp.Payload.AdminAccountEnabled {
		return nil, errors.New("server does not support admin user login")
	}

	authedAPI, authInfo, err := kargo.NewAPIWithCredential(opts.APIAddress, opts.Password, opts.InsecureSkipTLSVerify)
	if err != nil {
		return nil, err
	}
	loginResp, err := authedAPI.System.AdminLogin(
		system.NewAdminLoginParams().WithContext(ctx),
		authInfo,
	)
	if err != nil {
		return nil, fmt.Errorf("admin login: %w", err)
	}
	if loginResp.Payload == nil || loginResp.Payload.IDToken == "" {
		return nil, errors.New("admin login returned an empty token")
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	saved := &config.Context{
		Name:                  opts.ContextName,
		APIAddress:            opts.APIAddress,
		BearerToken:           loginResp.Payload.IDToken,
		InsecureSkipTLSVerify: opts.InsecureSkipTLSVerify,
		Project:               opts.Project,
	}
	cfg.Upsert(saved)
	if opts.MakeCurrent || cfg.CurrentContext == "" {
		cfg.CurrentContext = opts.ContextName
	}
	if err := config.Save(cfg); err != nil {
		return nil, err
	}
	return saved, nil
}

// defaultContextName derives a friendly context name from the API URL's host.
// Falls back to "default" if parsing fails.
func defaultContextName(apiAddress string) string {
	u, err := url.Parse(apiAddress)
	if err != nil || u.Host == "" {
		return "default"
	}
	return u.Hostname()
}

// readPassword prints prompt to stderr, then reads a line from r without
// echoing if r is a terminal.
func readPassword(r io.Reader, prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if f, ok := r.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
