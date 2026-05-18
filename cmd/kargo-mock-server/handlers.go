package main

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/cockroachdb/errors"
	"google.golang.org/protobuf/proto"
)

// readJSON decodes a JSON Connect-RPC request body into req.
func readJSON(r *http.Request, req any) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return errors.Wrap(err, "read JSON request body")
	}
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, req); err != nil {
		return errors.Wrap(err, "decode JSON request body")
	}
	return nil
}

// writeJSON writes a JSON Connect-RPC success response.
func writeJSON(w http.ResponseWriter, resp any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// readProto decodes a binary-protobuf Connect-RPC request body.
func readProto(r *http.Request, msg proto.Message) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return errors.Wrap(err, "read protobuf request body")
	}
	if len(body) == 0 {
		return nil
	}
	if err := proto.Unmarshal(body, msg); err != nil {
		return errors.Wrap(err, "decode protobuf request body")
	}
	return nil
}

// writeProto writes a binary-protobuf Connect-RPC success response.
func writeProto(w http.ResponseWriter, msg proto.Message) {
	body, err := proto.Marshal(msg)
	if err != nil {
		writeConnectError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/proto")
	_, _ = w.Write(body)
}

// writeConnectError emits a Connect-RPC JSON error envelope. Connect spec:
// {"code": "<code>", "message": "<msg>"}, with HTTP status mapped per
// https://connectrpc.com/docs/protocol/#error-codes.
func writeConnectError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

// notImplemented is the catch-all for RPCs the TUI never calls.
func (h *handlers) notImplemented(w http.ResponseWriter, r *http.Request) {
	writeConnectError(w, http.StatusNotImplemented, "unimplemented", "method "+r.URL.Path+" is not implemented by kargo-mock-server")
}

// Placeholder stubs — filled in by separate files but stubbed here so the
// route table compiles. Each real implementation lives in rpc_*.go.
