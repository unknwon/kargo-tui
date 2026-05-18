package kargo

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"

	"github.com/cockroachdb/errors"
	"google.golang.org/protobuf/proto"
)

// streamClient returns the long-lived HTTP client used for server-
// streaming Connect RPCs. Built on first use so the unary path doesn't
// pay for a second transport when no stream is in use.
func (c *connectJSON) streamClient() *http.Client {
	if c.streamHTTP != nil {
		return c.streamHTTP
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if t, ok := c.http.Transport.(*http.Transport); ok {
		// Mirror the unary transport's TLS config (matters when the
		// user opted into insecure-skip-verify on this context).
		transport.TLSClientConfig = t.TLSClientConfig
	}
	c.streamHTTP = &http.Client{Transport: transport}
	return c.streamHTTP
}

// callServerStream POSTs reqMsg to method with Connect's binary stream
// content type and invokes onMessage for every response frame the
// server sends. Returns when the stream ends (server-side close, error,
// or caller's context cancellation).
//
// onMessage receives a freshly-allocated respMsg-typed value via
// respFactory each call; if it returns a non-nil error, the stream is
// abandoned and that error propagates back from callServerStream.
//
// Connect-stream framing per frame:
//
//	byte 0    : flags (bit 1 = end-of-stream, bit 0 = compressed)
//	bytes 1-4 : big-endian uint32 message length
//	bytes 5-N : payload (proto for data frames; JSON envelope for the
//	            end-of-stream frame, which may carry a trailer error)
func (c *connectJSON) callServerStream(
	ctx context.Context,
	method string,
	reqMsg proto.Message,
	respFactory func() proto.Message,
	onMessage func(proto.Message) error,
) error {
	body, err := proto.Marshal(reqMsg)
	if err != nil {
		return errors.Wrapf(err, "marshal stream request for %s", method)
	}
	envelope := makeEnvelope(0, body)

	url := c.baseURL + kargoServicePath + method

	dial := func() (*http.Response, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(envelope))
		if err != nil {
			return nil, errors.Wrapf(err, "build stream request for %s", method)
		}
		httpReq.Header.Set("Content-Type", "application/connect+proto")
		httpReq.Header.Set("Accept", "application/connect+proto")
		httpReq.Header.Set("Connect-Protocol-Version", "1")
		if tok := c.bearer(); tok != "" {
			httpReq.Header.Set("Authorization", "Bearer "+tok)
		}
		return c.streamClient().Do(httpReq)
	}

	resp, err := dial()
	if err != nil {
		return errors.Wrapf(err, "call stream %s", method)
	}
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		dialErr := parseConnectError(method, resp.StatusCode, raw)
		if !isUnauthenticated(dialErr) || c.tryRefresh(ctx) != nil {
			return dialErr
		}
		// Refresh succeeded — retry the dial once. If the retry fails for
		// any reason, surface the original dialErr (the auth failure that
		// triggered the refresh) wrapped around the retry error so the
		// user sees both: "your token was bad, and the refreshed one
		// also got: <whatever>".
		resp, err = dial()
		if err != nil {
			return errors.Wrapf(err, "call stream %s after refresh. Original error: %v", method, dialErr)
		}
		if resp.StatusCode/100 != 2 {
			raw2, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return errors.Wrapf(parseConnectError(method, resp.StatusCode, raw2),
				"after refresh. Original error: %v", dialErr)
		}
	}
	defer resp.Body.Close()

	header := make([]byte, 5)
	for {
		if _, err := io.ReadFull(resp.Body, header); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return errors.Wrapf(err, "read stream %s frame header", method)
		}
		flags := header[0]
		length := binary.BigEndian.Uint32(header[1:5])
		if length > 32*1024*1024 {
			return errors.Newf("stream %s: implausibly large frame: %d bytes", method, length)
		}
		payload := make([]byte, length)
		if length > 0 {
			if _, err := io.ReadFull(resp.Body, payload); err != nil {
				return errors.Wrapf(err, "read stream %s frame payload", method)
			}
		}
		if flags&0x02 != 0 {
			if length == 0 {
				return nil
			}
			var trailer struct {
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(payload, &trailer); err == nil && trailer.Error != nil {
				return &connectError{
					Method:  method,
					Code:    trailer.Error.Code,
					Message: trailer.Error.Message,
				}
			}
			return nil
		}
		msg := respFactory()
		if err := proto.Unmarshal(payload, msg); err != nil {
			return errors.Wrapf(err, "decode stream %s frame", method)
		}
		if err := onMessage(msg); err != nil {
			return errors.Wrap(err, "handle stream message")
		}
	}
}

func makeEnvelope(flags byte, payload []byte) []byte {
	out := make([]byte, 5+len(payload))
	out[0] = flags
	binary.BigEndian.PutUint32(out[1:5], uint32(len(payload)))
	copy(out[5:], payload)
	return out
}
