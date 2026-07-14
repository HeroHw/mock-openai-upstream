package mockupstream

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// openai.go implements the OpenAI-compatible handlers: chat completions
// (streaming + non-streaming), embeddings, audio and models (doc §2.1, §5).

// extractPrompt concatenates the textual content of OpenAI `messages` for token
// estimation. It tolerates both string content and the array-of-parts form.
func extractPrompt(m map[string]any) string {
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
			for _, part := range c {
				if pm, ok := part.(map[string]any); ok {
					if t, ok := pm["text"].(string); ok {
						sb = append(sb, t...)
						sb = append(sb, ' ')
					}
				}
			}
		}
	}
	return string(sb)
}

// includeUsage reports whether the request asked for usage in the stream tail
// via stream_options.include_usage (doc §5.2).
func includeUsage(m map[string]any) bool {
	if so, ok := m["stream_options"].(map[string]any); ok {
		return boolField(so, "include_usage", false)
	}
	return false
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, _ := readBody(r)
	req := decodeJSON(body)
	model := strField(req, "model", "mock-model")

	// Deterministic error injection keyed by model + request sequence (§4.1).
	n := nextSeq()
	if shouldInject(fmt.Sprintf("%s#%d", model, n), s.cfg.ErrorRate) {
		openAIError(w, s.cfg.ErrorStatus, "server_error", "mock injected failure", "internal_error")
		return
	}

	prompt := extractPrompt(req)
	reply := s.replyText()
	reasoning := wantsReasoning(model, req)

	if boolField(req, "stream", false) {
		s.streamChat(w, r, model, prompt, reply, includeUsage(req), reasoning)
		return
	}

	// Non-streaming: apply overall latency then write a single completion.
	latency := randomDelay(fmt.Sprintf("%s#%d", model, n), s.cfg.LatencyMin, s.cfg.LatencyMax)
	if !sleepCtx(latency, clientGone(r)) {
		return // client disconnected
	}
	pt, ct, tt := s.usage(prompt, reply)
	message := map[string]any{"role": "assistant", "content": reply}
	if reasoning {
		// deepseek-v3.1 / qwen-*-thinking / doubao-seed 等思考模型在回包里
		// 额外携带 reasoning_content。
		message["reasoning_content"] = mockReasoningText
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      fmt.Sprintf("chatcmpl-mock-%d", n),
		"object":  "chat.completion",
		"created": 0,
		"model":   model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       message,
				"finish_reason": "stop",
			},
		},
		"usage": s.openAIUsage(pt, ct, tt),
	})
}

// streamChat emits OpenAI chat.completion.chunk SSE frames: an initial role
// delta, optional reasoning_content deltas (thinking models), one content delta
// per token at the configured interval, a finish frame, an optional usage
// frame, then [DONE] (doc §5.2).
func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, model, prompt, reply string, wantUsage, reasoning bool) {
	sse, ok := newSSE(w)
	if !ok {
		openAIError(w, http.StatusInternalServerError, "server_error", "streaming unsupported", "internal_error")
		return
	}
	done := clientGone(r)
	id := fmt.Sprintf("chatcmpl-mock-%d", nextSeq())

	// TTFT: wait before the very first frame (§4).
	ttft := randomDelay(id, s.cfg.TTFTMin, s.cfg.TTFTMax)
	if !sleepCtx(ttft, done) {
		return
	}

	// Initial role delta.
	if sse.data(chunkJSON(id, model, map[string]any{"role": "assistant"}, nil)) != nil {
		return
	}

	// Thinking phase: stream the reasoning text as reasoning_content deltas
	// before any content, matching deepseek/qwen-thinking/doubao streams.
	if reasoning {
		for _, tok := range splitTokens(mockReasoningText) {
			if !sleepCtx(s.cfg.TokenInterval, done) {
				return
			}
			if sse.data(chunkJSON(id, model, map[string]any{"reasoning_content": tok}, nil)) != nil {
				return
			}
		}
	}

	for _, tok := range splitTokens(reply) {
		if !sleepCtx(s.cfg.TokenInterval, done) {
			return
		}
		if sse.data(chunkJSON(id, model, map[string]any{"content": tok}, nil)) != nil {
			return
		}
	}

	// Finish frame.
	stop := "stop"
	if sse.data(chunkJSON(id, model, map[string]any{}, &stop)) != nil {
		return
	}

	// Optional usage frame in the stream tail (gateway billing depends on it).
	if wantUsage {
		pt, ct, tt := s.usage(prompt, reply)
		payload, _ := json.Marshal(map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": 0,
			"model":   model,
			"choices": []any{},
			"usage":   s.openAIUsage(pt, ct, tt),
		})
		if sse.data(string(payload)) != nil {
			return
		}
	}

	_ = sse.done()
}

// chunkJSON builds a single chat.completion.chunk frame body. finishReason is
// nil for content frames and set ("stop") for the terminal choice frame.
func chunkJSON(id, model string, delta map[string]any, finishReason *string) string {
	choice := map[string]any{
		"index": 0,
		"delta": delta,
	}
	if finishReason != nil {
		choice["finish_reason"] = *finishReason
	} else {
		choice["finish_reason"] = nil
	}
	b, _ := json.Marshal(map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   model,
		"choices": []any{choice},
	})
	return string(b)
}
