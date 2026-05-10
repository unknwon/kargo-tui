package kargo

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
)

// kargoServicePath is the URL path prefix of every method on the Kargo
// Connect-RPC service. The full URL is <baseURL><servicePath><MethodName>.
const kargoServicePath = "/akuity.io.kargo.service.v1alpha1.KargoService/"

// connectJSON speaks Connect-RPC over HTTP+JSON. Akuity-hosted Kargo (and
// Kargo's own server) supports this content type natively — no protobuf
// codec is required, which sidesteps the v2-protobuf-descriptor init panic
// in the generated KargoService client. We POST to
// <baseURL>/akuity.io.kargo.service.v1alpha1.KargoService/<Method> with a
// JSON body and decode the JSON response.
type connectJSON struct {
	baseURL string
	token   string
	http    *http.Client
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
// error envelope when available.
func (c *connectJSON) call(ctx context.Context, method string, req, out any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request for %s: %w", method, err)
	}
	url := c.baseURL + kargoServicePath + method
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request for %s: %w", method, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Connect-Protocol-Version", "1")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
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
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build proto request for %s: %w", method, err)
	}
	httpReq.Header.Set("Content-Type", "application/proto")
	httpReq.Header.Set("Accept", "application/proto")
	httpReq.Header.Set("Connect-Protocol-Version", "1")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
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
