// Package auth implements the SSO/OIDC login flow against a Kargo API server
// and writes the resulting credentials to kargo-tui's config file. It mirrors
// the upstream `kargo login --sso` flow: GetPublicConfig discovers the OIDC
// issuer, then a PKCE auth-code flow is run against the IdP through a local
// callback listener.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/pkg/browser"
	"go.opentelemetry.io/otel/attribute"
	"golang.org/x/oauth2"

	"unknwon.dev/kargo-tui/internal/config"
	"unknwon.dev/kargo-tui/internal/kargo"
	"unknwon.dev/kargo-tui/internal/tracing"
)

// LoginOptions captures the inputs to an SSO login flow.
type LoginOptions struct {
	APIAddress            string
	ContextName           string // defaults to host of APIAddress
	Project               string
	CallbackPort          int // 0 = pick an ephemeral free port
	InsecureSkipTLSVerify bool
	MakeCurrent           bool
	// Quiet suppresses progress prints to stderr — used when the login is
	// driven from inside the TUI's alt-screen, where stderr writes would
	// corrupt the rendered overlay.
	Quiet bool
}

// SSOLogin performs an OIDC PKCE auth-code flow and persists the resulting
// id_token (+ refresh token, if the IdP issues one) under a named context in
// the kargo-tui config file. Returns the saved context.
//
// status, if non-nil, is called with short progress strings ("Discovering
// OIDC provider…", "Open this URL in your browser: …") so callers can
// render progress to the user. It may be called from a goroutine other
// than the caller's.
func SSOLogin(ctx context.Context, opts LoginOptions, status func(string)) (*config.Context, error) {
	ctx, span := tracing.Start(ctx, "auth.SSOLogin",
		attribute.String("auth.context_name", opts.ContextName),
		attribute.String("auth.api_address", opts.APIAddress),
	)
	defer span.End()
	if status == nil {
		status = func(string) {}
	}
	if opts.APIAddress == "" {
		return nil, errors.New("API address is required")
	}
	if !strings.HasPrefix(opts.APIAddress, "http://") && !strings.HasPrefix(opts.APIAddress, "https://") {
		opts.APIAddress = "https://" + opts.APIAddress
	}
	if opts.ContextName == "" {
		opts.ContextName = defaultContextName(opts.APIAddress)
	}

	status("Talking to the Kargo server…")
	rpc := kargo.NewUnauthenticatedRPC(opts.APIAddress, opts.InsecureSkipTLSVerify)
	pub, err := rpc.GetPublicConfig(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "retrieve public config")
	}
	if pub != nil && pub.SkipAuth {
		// Server is auth-less (e.g. kargo-mock-server). Persist a context
		// with no token so subsequent RPCs go out with an empty Bearer.
		cfg, err := config.Load()
		if err != nil {
			return nil, errors.Wrap(err, "load config")
		}
		saved := &config.Context{
			Name:                  opts.ContextName,
			APIAddress:            opts.APIAddress,
			InsecureSkipTLSVerify: opts.InsecureSkipTLSVerify,
			Project:               opts.Project,
		}
		cfg.Upsert(saved)
		if opts.MakeCurrent || cfg.CurrentContext == "" {
			cfg.CurrentContext = opts.ContextName
		}
		if err := config.Save(cfg); err != nil {
			return nil, errors.Wrap(err, "save config")
		}
		return saved, nil
	}
	if pub == nil || pub.OIDC == nil {
		return nil, errors.New("server does not advertise OIDC configuration")
	}
	oidcCfg := pub.OIDC

	idToken, refreshToken, err := runPKCEFlow(ctx, oidcCfg, opts, status)
	if err != nil {
		return nil, errors.Wrap(err, "run PKCE flow")
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, errors.Wrap(err, "load config")
	}
	saved := &config.Context{
		Name:                  opts.ContextName,
		APIAddress:            opts.APIAddress,
		BearerToken:           idToken,
		RefreshToken:          refreshToken,
		InsecureSkipTLSVerify: opts.InsecureSkipTLSVerify,
		Project:               opts.Project,
	}
	cfg.Upsert(saved)
	if opts.MakeCurrent || cfg.CurrentContext == "" {
		cfg.CurrentContext = opts.ContextName
	}
	if err := config.Save(cfg); err != nil {
		return nil, errors.Wrap(err, "save config")
	}
	return saved, nil
}

// runPKCEFlow runs the OIDC authorization-code-with-PKCE flow against the
// configured IdP, opening the user's browser to consent and serving a local
// callback to capture the resulting authorization code.
func runPKCEFlow(ctx context.Context, cfg *kargo.OIDCConfig, opts LoginOptions, status func(string)) (string, string, error) {
	ctx, span := tracing.Start(ctx, "auth.PKCEFlow",
		attribute.String("auth.issuer", cfg.IssuerURL),
	)
	defer span.End()
	httpClient := &http.Client{}
	if opts.InsecureSkipTLSVerify {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
		httpClient.Transport = t
	}
	ctx = oidc.ClientContext(ctx, httpClient)

	status("Discovering OIDC provider " + cfg.IssuerURL + "…")
	discoveryCtx, cancelDiscovery := context.WithTimeout(ctx, 15*time.Second)
	provider, err := oidc.NewProvider(discoveryCtx, cfg.IssuerURL)
	cancelDiscovery()
	if err != nil {
		return "", "", errors.Wrapf(err, "init OIDC provider %s", cfg.IssuerURL)
	}

	scopes := append([]string{}, cfg.Scopes...)
	// Ask for offline_access if the provider supports it, so we can refresh.
	var providerClaims struct {
		ScopesSupported []string `json:"scopes_supported"`
	}
	if err := provider.Claims(&providerClaims); err == nil {
		const offlineAccess = "offline_access"
		if slices.Contains(providerClaims.ScopesSupported, offlineAccess) &&
			!slices.Contains(scopes, offlineAccess) {
			scopes = append(scopes, offlineAccess)
		}
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", opts.CallbackPort))
	if err != nil {
		return "", "", errors.Wrap(err, "start callback listener")
	}
	port := listener.Addr().(*net.TCPAddr).Port

	clientID := cfg.ClientID
	if cfg.CLIClientID != "" {
		clientID = cfg.CLIClientID
	}
	oauthCfg := oauth2.Config{
		ClientID:    clientID,
		Endpoint:    provider.Endpoint(),
		Scopes:      scopes,
		RedirectURL: fmt.Sprintf("http://localhost:%d/auth/callback", port),
	}

	state, err := randString(24)
	if err != nil {
		return "", "", errors.Wrap(err, "generate OAuth state")
	}
	verifier, challenge, err := pkceVerifierAndChallenge()
	if err != nil {
		return "", "", errors.Wrap(err, "generate PKCE verifier")
	}

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	srv := startCallbackServer(listener, state, codeCh, errCh)
	defer func() { _ = srv.Close() }()

	authURL := oauthCfg.AuthCodeURL(
		state,
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	status("Opening browser — if it doesn't, copy this URL: " + authURL)
	if !opts.Quiet {
		fmt.Fprintf(os.Stderr, "Opening browser to %s\nIf the browser doesn't open, visit that URL manually.\n", authURL)
	}
	if err := browser.OpenURL(authURL); err != nil && !opts.Quiet {
		// Non-fatal: user can still copy/paste the URL.
		fmt.Fprintf(os.Stderr, "(could not auto-open browser: %v)\n", err)
	}
	status("Waiting for sign-in callback…\n\nIf your browser didn't open, paste this URL into one:\n" + authURL)

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return "", "", errors.Wrap(err, "callback handler")
	case <-time.After(5 * time.Minute):
		return "", "", errors.New("timed out waiting for SSO sign-in")
	case <-ctx.Done():
		return "", "", errors.Wrap(ctx.Err(), "wait for SSO sign-in")
	}

	token, err := oauthCfg.Exchange(
		ctx,
		code,
		oauth2.SetAuthURLParam("code_verifier", verifier),
	)
	if err != nil {
		return "", "", errors.Wrap(err, "exchange auth code")
	}
	idToken, _ := token.Extra("id_token").(string)
	if idToken == "" {
		return "", "", errors.New("token response did not include id_token")
	}
	// Brief delay so the splash page assets finish loading before the local
	// server is shut down.
	time.Sleep(time.Second)
	return idToken, token.RefreshToken, nil
}

// startCallbackServer runs the OIDC callback receiver on listener. State is
// validated on each request; the first valid `code` is sent to codeCh and
// any error to errCh.
func startCallbackServer(listener net.Listener, state string, codeCh chan<- string, errCh chan<- error) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			select {
			case errCh <- errors.New("state mismatch in callback"):
			default:
			}
			return
		}
		if errStr := q.Get("error"); errStr != "" {
			desc := q.Get("error_description")
			http.Error(w, errStr+": "+desc, http.StatusBadRequest)
			select {
			case errCh <- errors.Newf("%s: %s", errStr, desc):
			default:
			}
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			select {
			case errCh <- errors.New("callback missing code"):
			default:
			}
			return
		}
		select {
		case codeCh <- code:
		default:
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(splashHTML))
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	return srv
}

const splashHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>kargo-tui · signed in</title>
<style>body{font-family:system-ui,sans-serif;background:#070707;color:#fbfbfb;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}main{text-align:center}h1{color:#009fff;margin:0 0 .5rem}p{color:#84848a;margin:0}</style>
</head><body><main><h1>You're signed in.</h1><p>You can close this tab and return to kargo-tui.</p></main></body></html>`

// pkceVerifierAndChallenge generates an RFC 7636 PKCE verifier (max length
// 128 chars from the unreserved set) and its S256 challenge.
func pkceVerifierAndChallenge() (string, string, error) {
	verifier, err := randStringFromCharset(128,
		"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~")
	if err != nil {
		return "", "", errors.Wrap(err, "generate PKCE verifier")
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// randString generates a cryptographically-random alphanumeric string. It's
// used for the OAuth `state` parameter, which only needs guess-resistance.
func randString(n int) (string, error) {
	return randStringFromCharset(n,
		"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789")
}

func randStringFromCharset(n int, charset string) (string, error) {
	b := make([]byte, n)
	max := big.NewInt(int64(len(charset)))
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", errors.Wrap(err, "generate random character")
		}
		b[i] = charset[idx.Int64()]
	}
	return string(b), nil
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
