package main

import (
	"encoding/binary"
	"io"
	"net/http"

	"google.golang.org/protobuf/proto"

	svcv1alpha1 "unknwon.dev/kargo-tui/internal/kargoapi/svc"
)

// watchStages opens a Connect server-stream of WatchStagesResponse frames.
// Per Connect's binary stream framing (see internal/kargo/stream.go for
// the consumer side):
//
//   - Each frame: [1 byte flags] [4 bytes big-endian length] [length bytes]
//   - flags bit 0: compression (we never set)
//   - flags bit 1: end-of-stream marker
//   - End-of-stream payload is a JSON envelope (we just send "{}")
func (h *handlers) watchStages(w http.ResponseWriter, r *http.Request) {
	req := &svcv1alpha1.WatchStagesRequest{}
	// Read the framed request envelope. Connect unary-to-stream-request
	// sends one envelope frame followed by EOF.
	body := streamFrameRead(r.Body)
	if body == nil {
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", "empty stream request")
		return
	}
	if err := proto.Unmarshal(body, req); err != nil {
		writeConnectError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}

	p, ok := h.store.project(req.Project)
	if !ok {
		writeConnectError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeConnectError(w, http.StatusInternalServerError, "internal", "streaming unsupported by responder")
		return
	}

	w.Header().Set("Content-Type", "application/connect+proto")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	h.store.mu.RLock()
	sub := p.subscribe()
	h.store.mu.RUnlock()
	defer p.unsubscribe(sub)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			writeEndOfStream(w, flusher)
			return
		case ev := <-sub.events:
			h.store.mu.RLock()
			payload, err := proto.Marshal(ev)
			h.store.mu.RUnlock()
			if err != nil {
				return
			}
			if err := writeStreamFrame(w, 0, payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeStreamFrame writes one Connect-stream frame: 1-byte flags + 4-byte
// big-endian length + payload.
func writeStreamFrame(w http.ResponseWriter, flags byte, payload []byte) error {
	header := make([]byte, 5)
	header[0] = flags
	binary.BigEndian.PutUint32(header[1:5], uint32(len(payload)))
	if _, err := w.Write(header); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// writeEndOfStream emits an EoS frame with an empty trailer. The TUI
// treats no `error` field as a clean close.
func writeEndOfStream(w http.ResponseWriter, flusher http.Flusher) {
	_ = writeStreamFrame(w, 0x02, []byte("{}"))
	flusher.Flush()
}

// streamFrameRead reads one Connect-stream frame from a request body and
// returns its payload. Returns nil on any error or short read.
func streamFrameRead(body io.Reader) []byte {
	header := make([]byte, 5)
	if _, err := io.ReadFull(body, header); err != nil {
		return nil
	}
	length := binary.BigEndian.Uint32(header[1:5])
	if length == 0 {
		return []byte{}
	}
	if length > 32*1024*1024 {
		return nil
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(body, payload); err != nil {
		return nil
	}
	return payload
}
