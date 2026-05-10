package auth

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"unknwon.dev/kargo-tui/internal/config"
	"unknwon.dev/kargo-tui/internal/kargo"
)

// Refresher swaps an expired OIDC id_token for a fresh one using the saved
// refresh_token, and persists the rotated tokens back to the config file so
// the next process start (and other in-flight callers) see the new values.
//
// It is safe for concurrent use, and additionally *coalesces* concurrent
// refresh attempts: a sync.Mutex serialises callers, and within the
// coalesceWindow after a successful exchange the cached id_token is
// returned to subsequent callers without re-exchanging. This matters
// because most IdPs rotate the refresh_token on every exchange — without
// coalescing, a burst of N parallel 401s would issue N exchanges back to
// back, and exchanges 2..N would fail with `invalid_grant` because they'd
// be presenting a now-stale refresh_token (the one they read from disk
// before exchange #1 wrote the new one).
type Refresher struct {
	contextName string
	insecure    bool

	mu       sync.Mutex
	provider *oidc.Provider // lazily discovered on first refresh
	clientID string         // from kargo PublicConfig (lazy)

	// lastToken / lastAt cache the most recent successful exchange so
	// coalesced callers can be served without re-hitting the IdP. lastAt
	// is the wall-clock time of the exchange; callers arriving within
	// coalesceWindow of it get lastToken back.
	lastToken string
	lastAt    time.Time
}

// coalesceWindow is how long after a successful exchange subsequent
// Refresh() callers receive the cached id_token instead of triggering a
// new exchange. Sized for "burst of parallel 401s from a single tick"
// (sub-second in practice), well under any reasonable id_token lifetime.
const coalesceWindow = 10 * time.Second

// NewRefresher builds a Refresher bound to a named config context. The
// RefreshToken field is read fresh from disk on every Refresh() so a
// token rotated by another process (e.g. a concurrent kargo-tui) is
// picked up. The OIDC provider (issuer URL, endpoints, client ID) is
// discovered on the first Refresh() and cached for the process lifetime
// — a server-side OIDC reconfiguration won't take effect until restart.
func NewRefresher(contextName string, insecureSkipTLSVerify bool) *Refresher {
	return &Refresher{contextName: contextName, insecure: insecureSkipTLSVerify}
}

// Refresh exchanges the saved refresh_token for a new id_token and writes
// both back to the config file. Returns the new id_token. The caller is
// expected to update its in-memory copy (e.g. the connectJSON token field).
//
// Coalesces concurrent callers: within coalesceWindow of a successful
// exchange, additional callers receive the cached id_token without a new
// IdP round trip — see the type-level doc for why this matters with
// rotating refresh tokens.
func (r *Refresher) Refresh(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.lastToken != "" && time.Since(r.lastAt) < coalesceWindow {
		return r.lastToken, nil
	}

	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("load config for refresh: %w", err)
	}
	cctx := cfg.Find(r.contextName)
	if cctx == nil {
		return "", fmt.Errorf("context %q not found", r.contextName)
	}
	if cctx.RefreshToken == "" {
		return "", errors.New("no refresh token saved; re-run `kargo-tui auth login`")
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	if r.insecure {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
		httpClient.Transport = t
	}
	oidcCtx := oidc.ClientContext(ctx, httpClient)

	if r.provider == nil {
		rpc := kargo.NewUnauthenticatedRPC(cctx.APIAddress, r.insecure)
		pub, err := rpc.GetPublicConfig(ctx)
		if err != nil {
			return "", fmt.Errorf("retrieve public config for refresh: %w", err)
		}
		if pub == nil || pub.OIDC == nil {
			return "", errors.New("server no longer advertises OIDC config")
		}
		discoveryCtx, cancel := context.WithTimeout(oidcCtx, 15*time.Second)
		provider, err := oidc.NewProvider(discoveryCtx, pub.OIDC.IssuerURL)
		cancel()
		if err != nil {
			return "", fmt.Errorf("init OIDC provider for refresh: %w", err)
		}
		r.provider = provider
		r.clientID = pub.OIDC.ClientID
		if pub.OIDC.CLIClientID != "" {
			r.clientID = pub.OIDC.CLIClientID
		}
	}

	oauthCfg := oauth2.Config{
		ClientID: r.clientID,
		Endpoint: r.provider.Endpoint(),
	}
	tokSource := oauthCfg.TokenSource(oidcCtx, &oauth2.Token{RefreshToken: cctx.RefreshToken})
	newTok, err := tokSource.Token()
	if err != nil {
		return "", fmt.Errorf("refresh token exchange: %w", err)
	}
	idToken, _ := newTok.Extra("id_token").(string)
	if idToken == "" {
		return "", errors.New("refresh response did not include id_token")
	}

	cctx.BearerToken = idToken
	// Some IdPs rotate refresh tokens; keep the old one if the response omits one.
	if newTok.RefreshToken != "" {
		cctx.RefreshToken = newTok.RefreshToken
	}
	if err := config.Save(cfg); err != nil {
		return "", fmt.Errorf("persist refreshed tokens: %w", err)
	}
	r.lastToken = idToken
	r.lastAt = time.Now()
	return idToken, nil
}
