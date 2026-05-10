// Package kargo wraps the Kargo OpenAPI client to read Stages, Freight,
// Promotions, Projects and supporting data, returning flattened, UI-friendly
// types tailored for the TUI. The Kargo Connect-RPC client is currently
// unusable as a standalone Go module (it requires v2 protobuf descriptors for
// k8s.io/api/core/v1 types that aren't shipped), so we use the upstream
// OpenAPI/Swagger client which speaks the same REST API as `kargo` CLI.
package kargo

import (
	"crypto/tls"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/go-openapi/runtime"
	httptransport "github.com/go-openapi/runtime/client"
	"github.com/go-openapi/strfmt"

	apiclient "github.com/akuity/kargo/pkg/client/generated"

	"unknwon.dev/kargo-tui/internal/config"
)

// Client wraps the Kargo OpenAPI client. It carries a default project so
// callers don't have to thread the project string through every call site,
// plus the bearer token for outgoing requests.
type Client struct {
	api      *apiclient.KargoAPI
	authInfo runtime.ClientAuthInfoWriter
	project  string
	baseURL  string
}

// NewClient builds an OpenAPI client for the configured Kargo context. The
// returned Client uses the context's bearer token for every request and the
// context's default project when the caller doesn't override it.
func NewClient(ctx *config.Context) (*Client, error) {
	api, authInfo, err := newAPIClient(ctx.APIAddress, ctx.BearerToken, ctx.InsecureSkipTLSVerify)
	if err != nil {
		return nil, err
	}
	return &Client{
		api:      api,
		authInfo: authInfo,
		project:  ctx.Project,
		baseURL:  strings.TrimRight(ctx.APIAddress, "/"),
	}, nil
}

// NewUnauthenticatedAPI is used by `auth login` to call AdminLogin and
// GetPublicConfig before any token has been issued. It returns the bare
// OpenAPI client; the caller passes the password as the "credential" so it
// gets placed in the Bearer header (which is what AdminLogin expects).
func NewUnauthenticatedAPI(apiAddress string, insecureSkipTLSVerify bool) (*apiclient.KargoAPI, error) {
	api, _, err := newAPIClient(apiAddress, "", insecureSkipTLSVerify)
	return api, err
}

// NewAPIWithCredential builds a client whose Authorization header is set to
// the given credential string. Used during the AdminLogin handshake where the
// admin password is passed as the "bearer" credential.
func NewAPIWithCredential(apiAddress, credential string, insecureSkipTLSVerify bool) (*apiclient.KargoAPI, runtime.ClientAuthInfoWriter, error) {
	return newAPIClient(apiAddress, credential, insecureSkipTLSVerify)
}

// newAPIClient is the shared transport setup for both authenticated and
// unauthenticated clients.
func newAPIClient(apiAddress, credential string, insecureSkipTLSVerify bool) (*apiclient.KargoAPI, runtime.ClientAuthInfoWriter, error) {
	u, err := url.Parse(strings.TrimRight(apiAddress, "/"))
	if err != nil {
		return nil, nil, err
	}
	if u.Host == "" {
		return nil, nil, &url.Error{Op: "parse", URL: apiAddress, Err: errInvalidURL}
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "https"
	}

	httpClient := &http.Client{}
	if insecureSkipTLSVerify {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
		httpClient.Transport = t
	}

	transport := httptransport.NewWithClient(u.Host, "/", []string{scheme}, httpClient)
	api := apiclient.New(transport, strfmt.Default)

	var authInfo runtime.ClientAuthInfoWriter
	if credential != "" {
		authInfo = httptransport.BearerToken(credential)
	}
	return api, authInfo, nil
}

// errInvalidURL is returned when the user supplies an API address that
// doesn't parse to a usable host. Wrapped in a url.Error for consistency.
var errInvalidURL = &simpleError{"missing host in URL"}

type simpleError struct{ s string }

func (e *simpleError) Error() string { return e.s }

// API returns the underlying OpenAPI client. Sub-package functions use this
// to call individual operations.
func (c *Client) API() *apiclient.KargoAPI { return c.api }

// AuthInfo returns the bearer auth writer for the configured context.
func (c *Client) AuthInfo() runtime.ClientAuthInfoWriter { return c.authInfo }

// Project returns the default project for this client.
func (c *Client) Project() string { return c.project }

// SetProject overrides the default project. Used when the picker switches
// projects mid-session.
func (c *Client) SetProject(p string) { c.project = p }

// BaseURL returns the Kargo API server URL backing this client.
func (c *Client) BaseURL() string { return c.baseURL }

// mapKeys returns the sorted keys of any string-keyed map.
func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

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

// stringPtr returns a pointer to s. Used for OpenAPI optional parameters.
func stringPtr(s string) *string { return &s }
