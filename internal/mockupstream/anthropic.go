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
// prompt-caching semantics (doc §5). cache_creation_input_tokens is the legacy
// total; the nested cache_creation object breaks it down by TTL
// (ephemeral_5m_input_tokens at 1.25x, ephemeral_1h_input_tokens at 2x), and
// the total always equals their sum, as in real responses. All cache fields
// are always present so the gateway's billing pipeline sees a stable shape;
// they are 0 when unconfigured.
func (s *Server) anthropicUsage(inputTokens, outputTokens int) map[string]any {
	c5m, c1h := s.cacheCreationSplit()
	return map[string]any{
		"input_tokens":                inputTokens,
		"output_tokens":               outputTokens,
		"cache_read_input_tokens":     s.cfg.CacheReadTokens,
		"cache_creation_input_tokens": c5m + c1h,
		"cache_creation": map[string]any{
			"ephemeral_5m_input_tokens": c5m,
			"ephemeral_1h_input_tokens": c1h,
		},
	}
}

// anthropicError writes an Anthropic-style error envelope
// ({"type":"error","error":{"type","message"}}).
func anthropicError(w http.ResponseWriter, status int, errType, message string) {
	writeJSON(w, status, map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errType,
			"message": message,
		},
	})
}

// strictAnthropicCheck mimics real /v1/messages validation (MOCK_STRICT=1)：
// max_tokens 必填且 ≥1；thinking 开启时 budget_tokens 必须 ≥1024 且严格小于
// max_tokens。网关的 "缺省 MaxTokens 补全" 和 "-thinking 适配预算折算" 没
// 生效时，这里必然报 400。
func strictAnthropicCheck(req map[string]any) string {
	maxTokens, hasMax := req["max_tokens"]
	mt := intField(req, "max_tokens", 0)
	if !hasMax || maxTokens == nil {
		return "max_tokens: Field required"
	}
	if mt < 1 {
		return "max_tokens: Input should be greater than or equal to 1"
	}
	if th, ok := req["thinking"].(map[string]any); ok && strField(th, "type", "") == "enabled" {
		bt := intField(th, "budget_tokens", 0)
		if bt < 1024 {
			return "thinking.enabled.budget_tokens: Input should be greater than or equal to 1024"
		}
		if bt >= mt {
			return fmt.Sprintf("`max_tokens` must be greater than `thinking.budget_tokens`. "+
				"Please adjust your `max_tokens` (currently %d) to be greater than `thinking.budget_tokens` (currently %d).", mt, bt)
		}
		// 真实上游：thinking 开启时 temperature 只能为 1（网关 -thinking 适配
		// 的 "强制 temperature=1.0" 改写点由此得到端到端验证）。
		if temp, ok := req["temperature"].(float64); ok && temp != 1 {
			return "`temperature` may only be set to 1 when thinking is enabled. " +
				"Please consult our documentation at https://docs.anthropic.com/en/docs/build-with-claude/extended-thinking"
		}
	}
	return ""
}

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	body, _ := readBody(r)
	req := decodeJSON(body)
	model := strField(req, "model", "mock-claude")

	if s.cfg.Strict {
		if msg := strictAnthropicCheck(req); msg != "" {
			anthropicError(w, http.StatusBadRequest, "invalid_request_error", msg)
			return
		}
	}

	n := nextSeq()
	if shouldInject(fmt.Sprintf("%s#%d", model, n), s.cfg.ErrorRate) {
		// Anthropic error envelope.
		anthropicError(w, s.cfg.ErrorStatus, "api_error", "mock injected failure")
		return
	}

	prompt := extractAnthropicPrompt(req)
	reply := s.replyText()
	pt, ct, _ := s.usage(prompt, reply)
	msgID := fmt.Sprintf("msg-mock-%d", n)
	// Extended thinking（claude-fable-5 / claude-opus-4-8）：请求携带
	// {"thinking":{"type":"enabled",...}} 时，回包在 text 块前多一个 thinking 块。
	thinking := wantsReasoning(model, req)

	if boolField(req, "stream", false) {
		s.streamAnthropic(w, r, msgID, model, reply, pt, ct, thinking)
		return
	}

	if !sleepCtx(randomDelay(msgID, s.cfg.LatencyMin, s.cfg.LatencyMax), clientGone(r)) {
		return
	}
	content := []any{}
	if thinking {
		content = append(content, map[string]any{
			"type":      "thinking",
			"thinking":  mockReasoningText,
			"signature": "mock-signature",
		})
	}
	content = append(content, map[string]any{"type": "text", "text": reply})
	writeJSON(w, http.StatusOK, map[string]any{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         s.anthropicUsage(pt, ct),
	})
}

// streamAnthropic emits the Anthropic streaming event sequence: message_start,
// [thinking block: content_block_start/delta(thinking_delta)/stop,] then the
// text block content_block_start, content_block_delta (per token),
// content_block_stop, message_delta (with usage), message_stop (doc §5.3).
func (s *Server) streamAnthropic(w http.ResponseWriter, r *http.Request, msgID, model, reply string, pt, ct int, thinking bool) {
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

	// The text block index shifts to 1 when a thinking block occupies index 0.
	textIndex := 0
	if thinking {
		textIndex = 1
		thinkStart, _ := json.Marshal(map[string]any{
			"type":          "content_block_start",
			"index":         0,
			"content_block": map[string]any{"type": "thinking", "thinking": ""},
		})
		if sse.event("content_block_start", string(thinkStart)) != nil {
			return
		}
		for _, tok := range splitTokens(mockReasoningText) {
			if !sleepCtx(s.cfg.TokenInterval, done) {
				return
			}
			delta, _ := json.Marshal(map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{"type": "thinking_delta", "thinking": tok},
			})
			if sse.event("content_block_delta", string(delta)) != nil {
				return
			}
		}
		sig, _ := json.Marshal(map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{"type": "signature_delta", "signature": "mock-signature"},
		})
		if sse.event("content_block_delta", string(sig)) != nil {
			return
		}
		thinkStop, _ := json.Marshal(map[string]any{"type": "content_block_stop", "index": 0})
		if sse.event("content_block_stop", string(thinkStop)) != nil {
			return
		}
	}

	blockStart, _ := json.Marshal(map[string]any{
		"type":          "content_block_start",
		"index":         textIndex,
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
			"index": textIndex,
			"delta": map[string]any{"type": "text_delta", "text": tok},
		})
		if sse.event("content_block_delta", string(delta)) != nil {
			return
		}
	}

	blockStop, _ := json.Marshal(map[string]any{"type": "content_block_stop", "index": textIndex})
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
