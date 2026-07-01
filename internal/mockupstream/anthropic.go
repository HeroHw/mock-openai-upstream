package mockupstream

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// anthropic.go implements the Anthropic native /v1/messages endpoint. Streaming
// uses named SSE event frames distinct from OpenAI's format (doc §5.3).

// extractAnthropicPrompt concatenates text from Anthropic `messages` content,
// which may be a string or an array of typed content blocks.
func extractAnthropicPrompt(m map[string]any) string {
	msgs, ok := m["messages"].([]any)
	if !ok {
		return ""
	}
	var sb []byte
	for _, mi := range msgs {
		msg, ok := mi.(map[string]any)
		if !ok {
			continue
		}
		switch c := msg["content"].(type) {
		case string:
			sb = append(sb, c...)
			sb = append(sb, ' ')
		case []any:
			for _, blk := range c {
				if bm, ok := blk.(map[string]any); ok {
					if t, ok := bm["text"].(string); ok {
						sb = append(sb, t...)
						sb = append(sb, ' ')
					}
				}
			}
		}
	}
	return string(sb)
}

// anthropicUsage builds the usage block for the Anthropic message envelope.
// input_tokens is the full-price remainder; cache_read_input_tokens (~0.1x base
// price) and cache_creation_input_tokens (1.25x/2x base price) are reported
// separately and are NOT folded into input_tokens, matching real upstream
// prompt-caching semantics (doc §5). Both cache fields are always present so the
// gateway's billing pipeline sees a stable shape; they are 0 when unconfigured.
func (s *Server) anthropicUsage(inputTokens, outputTokens int) map[string]any {
	return map[string]any{
		"input_tokens":                inputTokens,
		"output_tokens":               outputTokens,
		"cache_read_input_tokens":     s.cfg.CacheReadTokens,
		"cache_creation_input_tokens": s.cfg.CacheCreationTokens,
	}
}

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	body, _ := readBody(r)
	req := decodeJSON(body)
	model := strField(req, "model", "mock-claude")

	n := nextSeq()
	if shouldInject(fmt.Sprintf("%s#%d", model, n), s.cfg.ErrorRate) {
		// Anthropic error envelope.
		writeJSON(w, s.cfg.ErrorStatus, map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "api_error",
				"message": "mock injected failure",
			},
		})
		return
	}

	prompt := extractAnthropicPrompt(req)
	reply := s.replyText()
	pt, ct, _ := s.usage(prompt, reply)
	msgID := fmt.Sprintf("msg-mock-%d", n)

	if boolField(req, "stream", false) {
		s.streamAnthropic(w, r, msgID, model, reply, pt, ct)
		return
	}

	if !sleepCtx(randomDelay(msgID, s.cfg.LatencyMin, s.cfg.LatencyMax), clientGone(r)) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       []any{map[string]any{"type": "text", "text": reply}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         s.anthropicUsage(pt, ct),
	})
}

// streamAnthropic emits the Anthropic streaming event sequence: message_start,
// content_block_start, content_block_delta (per token), content_block_stop,
// message_delta (with usage), message_stop (doc §5.3).
func (s *Server) streamAnthropic(w http.ResponseWriter, r *http.Request, msgID, model, reply string, pt, ct int) {
	sse, ok := newSSE(w)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	done := clientGone(r)

	if !sleepCtx(randomDelay(msgID, s.cfg.TTFTMin, s.cfg.TTFTMax), done) {
		return
	}

	start, _ := json.Marshal(map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         s.anthropicUsage(pt, 0),
		},
	})
	if sse.event("message_start", string(start)) != nil {
		return
	}

	blockStart, _ := json.Marshal(map[string]any{
		"type":          "content_block_start",
		"index":         0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	if sse.event("content_block_start", string(blockStart)) != nil {
		return
	}

	for _, tok := range splitTokens(reply) {
		if !sleepCtx(s.cfg.TokenInterval, done) {
			return
		}
		delta, _ := json.Marshal(map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{"type": "text_delta", "text": tok},
		})
		if sse.event("content_block_delta", string(delta)) != nil {
			return
		}
	}

	blockStop, _ := json.Marshal(map[string]any{"type": "content_block_stop", "index": 0})
	if sse.event("content_block_stop", string(blockStop)) != nil {
		return
	}

	msgDelta, _ := json.Marshal(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": s.anthropicUsage(pt, ct),
	})
	if sse.event("message_delta", string(msgDelta)) != nil {
		return
	}

	stop, _ := json.Marshal(map[string]any{"type": "message_stop"})
	_ = sse.event("message_stop", string(stop))
}
