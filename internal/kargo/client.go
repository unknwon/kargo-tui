// Package kargo wraps the Kargo API to read Stages, Freight, Promotions,
// Projects and supporting data, returning flattened, UI-friendly types
// tailored for the TUI.
//
// The Kargo Connect-RPC client generated under github.com/akuity/kargo/api
// panics at init() because it depends on v2 protobuf descriptors for
// k8s.io/api/core/v1 types that aren't shipped as a standalone module. The
// OpenAPI/Swagger client at github.com/akuity/kargo/pkg/client/generated
// works against locally-installed Kargo, but Akuity-hosted Kargo only
// exposes the Connect-RPC surface (the REST gateway path returns 405).
//
// To support both, this package speaks Connect-RPC over HTTP+JSON directly:
// POST <baseURL>/akuity.io.kargo.service.v1alpha1.KargoService/<Method>
// with a JSON body and a JSON response. No protobuf reflection is involved
// (sidestepping the init panic), and the wire format is identical to what
// the official server emits (sidestepping the missing-REST issue).
package kargo

import (
	"context"
	"strings"
	"time"

	"github.com/cockroachdb/errors"

	"unknwon.dev/kargo-tui/internal/config"
)

// Client wraps the Kargo Connect-RPC-over-JSON transport. It carries a
// default project so callers don't have to thread the project string
// through every call site, plus the bearer token for outgoing requests.
type Client struct {
	rpc     *connectJSON
	project string
	baseURL string
}

// NewClient builds a client for the configured Kargo context. The returned
// Client uses the context's bearer token for every request and the
// context's default project when the caller doesn't override it.
func NewClient(ctx *config.Context) (*Client, error) {
	rpc := newConnectJSON(ctx.APIAddress, ctx.BearerToken, ctx.InsecureSkipTLSVerify)
	return &Client{
		rpc:     rpc,
		project: ctx.Project,
		baseURL: strings.TrimRight(ctx.APIAddress, "/"),
	}, nil
}

// SetTokenRefresher installs a callback the transport invokes after a
// CodeUnauthenticated response, to obtain a fresh bearer token before
// retrying. Pass nil to disable. Wired here rather than at construction
// to avoid an import cycle between the auth and kargo packages.
//
// Must be called before any RPC is issued on this client: the field
// write is unsynchronized, so racing it against in-flight RPCs (which
// read c.rpc.refresh in tryRefresh) is a data race.
func (c *Client) SetTokenRefresher(refresh func(context.Context) (string, error)) {
	c.rpc.refresh = refresh
}

// PrimeAsync arms the transport's bootstrap gate synchronously and
// spawns a goroutine to refresh the token. Every RPC issued between
// PrimeAsync's return and the goroutine's completion blocks on the
// gate, so the first burst of post-priming calls (Init fan-out,
// context-switch fan-out) sees the freshly-refreshed bearer instead
// of racing it. No-op when the refresher hasn't been attached yet.
//
// The supplied timeout caps how long the goroutine waits on the IdP
// before giving up; on timeout the gate releases and the affected
// RPCs proceed with the old token (and recover via the lazy 401
// retry, same as if PrimeAsync had never been called).
func (c *Client) PrimeAsync(timeout time.Duration) {
	if c.rpc.refresh == nil {
		return
	}
	if !c.rpc.BeginBootstrap() {
		return
	}
	go func() {
		defer c.rpc.EndBootstrap()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		_ = c.rpc.tryRefresh(ctx)
	}()
}

// ForceRefresh proactively invokes the configured token refresher and
// updates the in-memory bearer. Used at startup (and on context
// switch) when the saved id_token is already known to be expired (or
// about to be), so the first RPC doesn't have to eat a 401 and
// recover. Arms the transport's bootstrap gate so concurrent RPCs
// block until the refresh completes, preventing the race that would
// otherwise burn the first burst of calls on a stale bearer. No-op
// when no refresher is attached.
func (c *Client) ForceRefresh(ctx context.Context) error {
	owned := c.rpc.BeginBootstrap()
	if owned {
		defer c.rpc.EndBootstrap()
	}
	if err := c.rpc.tryRefresh(ctx); err != nil {
		return errors.Wrap(err, "force token refresh")
	}
	return nil
}

// NewUnauthenticatedRPC is used by `auth login` to call AdminLogin and
// GetPublicConfig before any token has been issued.
func NewUnauthenticatedRPC(apiAddress string, insecureSkipTLSVerify bool) *connectJSONWrapper {
	return &connectJSONWrapper{rpc: newConnectJSON(apiAddress, "", insecureSkipTLSVerify)}
}

// connectJSONWrapper is a tiny adapter that exposes a couple of pre-auth
// methods to the auth package without leaking the unexported transport.
type connectJSONWrapper struct{ rpc *connectJSON }

// PublicConfig is the subset of GetPublicConfigResponse we care about
// during the login flow.
type PublicConfig struct {
	OIDC                *OIDCConfig `json:"oidcConfig,omitempty"`
	AdminAccountEnabled bool        `json:"adminAccountEnabled,omitempty"`
	SkipAuth            bool        `json:"skipAuth,omitempty"`
}

// OIDCConfig mirrors the Kargo server's published OIDC client config.
type OIDCConfig struct {
	IssuerURL   string   `json:"issuerUrl"`
	ClientID    string   `json:"clientId"`
	CLIClientID string   `json:"cliClientId"`
	Scopes      []string `json:"scopes"`
}

// GetPublicConfig calls the unauthenticated GetPublicConfig RPC.
func (w *connectJSONWrapper) GetPublicConfig(ctx context.Context) (*PublicConfig, error) {
	var out PublicConfig
	if err := w.rpc.call(ctx, "GetPublicConfig", struct{}{}, &out); err != nil {
		return nil, errors.Wrap(err, "get public config")
	}
	return &out, nil
}

// AdminLogin calls AdminLogin with the given password and returns the
// issued bearer/id token.
func (w *connectJSONWrapper) AdminLogin(ctx context.Context, password string) (string, error) {
	req := struct {
		Password string `json:"password"`
	}{Password: password}
	var resp struct {
		IDToken string `json:"idToken"`
	}
	if err := w.rpc.call(ctx, "AdminLogin", req, &resp); err != nil {
		return "", errors.Wrap(err, "admin login")
	}
	return resp.IDToken, nil
}

// Project returns the default project for this client.
func (c *Client) Project() string { return c.project }

// SetProject overrides the default project. Used when the picker switches
// projects mid-session.
func (c *Client) SetProject(p string) { c.project = p }

// BaseURL returns the Kargo API server URL backing this client.
func (c *Client) BaseURL() string { return c.baseURL }

// pickString returns the first non-empty string field at the given keys, or
// "" if none are present. Used to tolerate inconsistent casing in Kargo's
// health-output JSON.
func pickString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
