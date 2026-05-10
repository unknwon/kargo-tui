package kargo

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
)

// kargoServicePath is the URL path prefix of every method on the Kargo
// Connect-RPC service. The full URL is <baseURL><servicePath><MethodName>.
const kargoServicePath = "/akuity.io.kargo.service.v1alpha1.KargoService/"

// connectJSON is the hand-rolled Connect-RPC transport used to talk to
// Kargo without importing the upstream-generated client (which panics
// at init() because of v2-protobuf-descriptor mismatches with the
// gogo-typed corev1 messages it embeds). The transport speaks three
// flavours of Connect on the same endpoint family
// (<baseURL>/akuity.io.kargo.service.v1alpha1.KargoService/<Method>):
//
//   - call() — unary application/json, used for projects, events, and
//     other RPCs that don't carry metav1.Time fields.
//   - callProto() — unary application/proto, used for stages and
//     promotions where the JSON encoder elides metav1.Time to "{}".
//   - callServerStream() — application/connect+proto, used for
//     WatchStages-style server-streaming RPCs.
type connectJSON struct {
	baseURL string

	tokenMu sync.RWMutex
	token   string
	// refresh, when set, is called after a CodeUnauthenticated response.
	// It must return a fresh bearer token and persist any rotation
	// externally; connectJSON updates its in-memory token from the
	// returned value before retrying the failed call once.
	refresh func(context.Context) (string, error)

	http *http.Client
	// streamHTTP is a separate client without a request timeout, used
	// for long-lived server-streaming RPCs (WatchStages etc.). Built
	// lazily by streamClient().
	streamHTTP *http.Client
}

// newConnectJSON builds a hand-rolled Kargo Connect-RPC-over-JSON client.
// baseURL must include the scheme (https://...) and host; the trailing
// slash is trimmed.
func newConnectJSON(baseURL, token string, insecureSkipTLSVerify bool) *connectJSON {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if insecureSkipTLSVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}
	return &connectJSON{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}
}

// call POSTs req as JSON to method and decodes the response into out. A
// non-2xx status is returned as a *connectError carrying the server's JSON
// error envelope when available. On CodeUnauthenticated the token is
// refreshed once (if a refresher is configured) and the call retried.
func (c *connectJSON) call(ctx context.Context, method string, req, out any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request for %s: %w", method, err)
	}
	url := c.baseURL + kargoServicePath + method

	doOnce := func() error {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("build request for %s: %w", method, err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "application/json")
		httpReq.Header.Set("Connect-Protocol-Version", "1")
		if tok := c.bearer(); tok != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := c.http.Do(httpReq)
		if err != nil {
			return fmt.Errorf("call %s: %w", method, err)
		}
		defer resp.Body.Close()
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read %s response: %w", method, err)
		}
		if resp.StatusCode/100 != 2 {
			return parseConnectError(method, resp.StatusCode, respBody)
		}
		if out == nil || len(respBody) == 0 {
			return nil
		}
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode %s response: %w", method, err)
		}
		return nil
	}

	err = doOnce()
	if isUnauthenticated(err) && c.tryRefresh(ctx) == nil {
		err = doOnce()
	}
	return err
}

// callProto is the binary-protobuf counterpart to call. The Kargo
// Connect-RPC server happily speaks `application/proto`, and crucially its
// proto encoder *does* materialise metav1.Time values into the wire bytes
// — unlike the JSON encoder, which elides every Time to `{}`. Use this for
// any RPC whose response carries timestamps the TUI needs to render
// honestly. Both reqMsg and respMsg must be proto.Message values; the
// vendored Kargo types live under internal/kargoapi/svc.
func (c *connectJSON) callProto(ctx context.Context, method string, reqMsg, respMsg proto.Message) error {
	body, err := proto.Marshal(reqMsg)
	if err != nil {
		return fmt.Errorf("marshal proto request for %s: %w", method, err)
	}
	url := c.baseURL + kargoServicePath + method

	doOnce := func() error {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("build proto request for %s: %w", method, err)
		}
		httpReq.Header.Set("Content-Type", "application/proto")
		httpReq.Header.Set("Accept", "application/proto")
		httpReq.Header.Set("Connect-Protocol-Version", "1")
		if tok := c.bearer(); tok != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := c.http.Do(httpReq)
		if err != nil {
			return fmt.Errorf("proto call %s: %w", method, err)
		}
		defer resp.Body.Close()
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read proto %s response: %w", method, err)
		}
		if resp.StatusCode/100 != 2 {
			// Connect's error envelope is JSON even on a proto request, so
			// reuse the JSON parser rather than inventing a second one.
			return parseConnectError(method, resp.StatusCode, respBody)
		}
		if respMsg == nil || len(respBody) == 0 {
			return nil
		}
		if err := proto.Unmarshal(respBody, respMsg); err != nil {
			return fmt.Errorf("decode proto %s response: %w", method, err)
		}
		return nil
	}

	err = doOnce()
	if isUnauthenticated(err) && c.tryRefresh(ctx) == nil {
		err = doOnce()
	}
	return err
}

// bearer returns the current token under a read lock so refresh() callers
// can rotate it without racing in-flight RPCs.
func (c *connectJSON) bearer() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.token
}

// tryRefresh invokes the configured refresher (if any) and updates the
// in-memory token on success. Returns the refresher's error so callers can
// decide whether to retry. Multiple concurrent callers are deduped by the
// refresher's own mutex; any extra calls past the first are cheap re-reads
// of the just-rotated token.
func (c *connectJSON) tryRefresh(ctx context.Context) error {
	if c.refresh == nil {
		return errNoRefresher
	}
	tok, err := c.refresh(ctx)
	if err != nil {
		return err
	}
	c.tokenMu.Lock()
	c.token = tok
	c.tokenMu.Unlock()
	return nil
}

var errNoRefresher = fmt.Errorf("no token refresher configured")

// isUnauthenticated reports whether err is a Connect error with the
// "unauthenticated" code (or HTTP 401 fallback for older servers).
func isUnauthenticated(err error) bool {
	if err == nil {
		return false
	}
	var ce *connectError
	if !errors.As(err, &ce) {
		return false
	}
	return ce.Code == "unauthenticated" || ce.Status == http.StatusUnauthorized
}

// IsUnauthenticated is the public counterpart used by callers (e.g. the TUI)
// to recognise "session expired" errors and surface a re-login prompt.
func IsUnauthenticated(err error) bool { return isUnauthenticated(err) }

// connectError matches the JSON shape Connect-RPC returns on errors:
// {"code":"unauthenticated","message":"...","details":[...]}. We surface
// just the code and message in Error() — the rest is rare-case noise.
type connectError struct {
	Method  string
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
	Body    string `json:"-"`
}

func (e *connectError) Error() string {
	if e.Code != "" || e.Message != "" {
		if e.Code != "" && e.Message != "" {
			return fmt.Sprintf("%s: %s: %s", e.Method, e.Code, e.Message)
		}
		if e.Message != "" {
			return fmt.Sprintf("%s: %s", e.Method, e.Message)
		}
		return fmt.Sprintf("%s: %s (HTTP %d)", e.Method, e.Code, e.Status)
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.Method, e.Status, strings.TrimSpace(e.Body))
}

// parseConnectError tries to decode the standard Connect error envelope; on
// failure it falls back to the raw body so the caller still sees something
// useful.
func parseConnectError(method string, status int, body []byte) error {
	ce := &connectError{Method: method, Status: status, Body: string(body)}
	_ = json.Unmarshal(body, ce)
	return ce
}
