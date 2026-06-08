package mockupstream

import (
	"fmt"
	"net/http"
)

// sse.go implements Server-Sent Events writing from scratch (Go stdlib only,
// doc §5.2/§5.3). It supports both OpenAI-style anonymous `data:` frames and
// Anthropic-style named `event:`/`data:` frames.

// sseWriter wraps an http.ResponseWriter for incremental SSE flushing.
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// newSSE sets the SSE response headers and returns a writer, or false if the
// ResponseWriter does not support flushing (cannot stream).
func newSSE(w http.ResponseWriter) (*sseWriter, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &sseWriter{w: w, flusher: flusher}, true
}

// data writes an anonymous SSE frame: "data: <payload>\n\n" and flushes.
func (s *sseWriter) data(payload string) error {
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", payload); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// event writes a named SSE frame: "event: <name>\ndata: <payload>\n\n".
func (s *sseWriter) event(name, payload string) error {
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, payload); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// done writes the OpenAI terminal sentinel frame "data: [DONE]".
func (s *sseWriter) done() error {
	return s.data("[DONE]")
}

// clientGone returns a channel closed when the client disconnects, so streaming
// loops can stop early instead of writing into a dead connection.
func clientGone(r *http.Request) <-chan struct{} {
	return r.Context().Done()
}
