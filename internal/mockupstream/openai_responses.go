package mockupstream

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// openai_responses.go implements the OpenAI Responses API (/v1/responses) —
// the successor protocol to chat/completions that gpt-5.x clients default to.
// Non-streaming returns a single `response` envelope whose output array holds
// an optional reasoning item plus a message item; streaming emits named SSE
// events (response.created → response.output_text.delta* → response.completed),
// unlike chat/completions' anonymous `data:` frames.

// extractResponsesPrompt concatenates the textual content of the Responses
// `input` field, which may be a bare string or an array of messages whose
// content is a string or an array of typed parts ({type:"input_text",text}).
func extractResponsesPrompt(m map[string]any) string {
	switch in := m["input"].(type) {
	case string:
		return in
	case []any:
		var sb []byte
		for _, mi := range in {
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
	return ""
}

// responsesWantsReasoning reports whether a Responses request should carry a
// reasoning output item: an explicit `reasoning` object in the request (gpt-5.x
// 风格 {"reasoning":{"effort":"high"}}), or the shared thinking heuristics.
func responsesWantsReasoning(model string, req map[string]any) bool {
	if _, ok := req["reasoning"].(map[string]any); ok {
		return true
	}
	return wantsReasoning(model, req)
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	body, _ := readBody(r)
	req := decodeJSON(body)
	model := strField(req, "model", "mock-model")

	// Deterministic error injection keyed by model + request sequence (§4.1).
	n := nextSeq()
	if shouldInject(fmt.Sprintf("%s#%d", model, n), s.cfg.ErrorRate) {
		openAIError(w, s.cfg.ErrorStatus, "server_error", "mock injected failure", "internal_error")
		return
	}

	prompt := extractResponsesPrompt(req)
	reply := s.replyText()
	reasoning := responsesWantsReasoning(model, req)
	respID := fmt.Sprintf("resp-mock-%d", n)

	if boolField(req, "stream", false) {
		s.streamResponses(w, r, respID, model, prompt, reply, reasoning)
		return
	}

	latency := randomDelay(fmt.Sprintf("%s#%d", model, n), s.cfg.LatencyMin, s.cfg.LatencyMax)
	if !sleepCtx(latency, clientGone(r)) {
		return // client disconnected
	}
	writeJSON(w, http.StatusOK, s.responsesEnvelope(respID, model, prompt, reply, reasoning, "completed"))
}

// responsesEnvelope builds the full `response` object shared by the
// non-streaming body and the response.created/completed stream events.
func (s *Server) responsesEnvelope(respID, model, prompt, reply string, reasoning bool, status string) map[string]any {
	pt, ct, tt := s.usage(prompt, reply)

	output := []any{}
	reasoningTokens := 0
	if reasoning {
		reasoningTokens = estimateTokens(mockReasoningText)
		output = append(output, map[string]any{
			"id":   respID + "-rs",
			"type": "reasoning",
			"summary": []any{
				map[string]any{"type": "summary_text", "text": mockReasoningText},
			},
		})
	}
	if status == "completed" {
		output = append(output, map[string]any{
			"id":     respID + "-msg",
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []any{
				map[string]any{"type": "output_text", "text": reply, "annotations": []any{}},
			},
		})
	}

	return map[string]any{
		"id":         respID,
		"object":     "response",
		"created_at": 0,
		"status":     status,
		"model":      model,
		"output":     output,
		"error":      nil,
		"usage": map[string]any{
			"input_tokens":  pt,
			"output_tokens": ct + reasoningTokens,
			"output_tokens_details": map[string]any{
				"reasoning_tokens": reasoningTokens,
			},
			"total_tokens": tt + reasoningTokens,
		},
	}
}

// streamResponses emits the Responses streaming event sequence:
// response.created, response.in_progress, response.output_item.added,
// response.content_part.added, response.output_text.delta (per token),
// response.output_text.done, response.content_part.done,
// response.output_item.done, response.completed. Each frame is a named SSE
// event carrying a monotonically increasing sequence_number.
func (s *Server) streamResponses(w http.ResponseWriter, r *http.Request, respID, model, prompt, reply string, reasoning bool) {
	sse, ok := newSSE(w)
	if !ok {
		openAIError(w, http.StatusInternalServerError, "server_error", "streaming unsupported", "internal_error")
		return
	}
	done := clientGone(r)

	if !sleepCtx(randomDelay(respID, s.cfg.TTFTMin, s.cfg.TTFTMax), done) {
		return
	}

	seqNo := 0
	emit := func(event string, fields map[string]any) error {
		fields["type"] = event
		fields["sequence_number"] = seqNo
		seqNo++
		b, _ := json.Marshal(fields)
		return sse.event(event, string(b))
	}

	inProgress := s.responsesEnvelope(respID, model, prompt, reply, reasoning, "in_progress")
	if emit("response.created", map[string]any{"response": inProgress}) != nil {
		return
	}
	if emit("response.in_progress", map[string]any{"response": inProgress}) != nil {
		return
	}

	outputIndex := 0
	// Reasoning item first: gpt-5.x streams emit the reasoning output item
	// (with summary deltas) before the message item.
	if reasoning {
		rsID := respID + "-rs"
		if emit("response.output_item.added", map[string]any{
			"output_index": outputIndex,
			"item":         map[string]any{"id": rsID, "type": "reasoning", "summary": []any{}},
		}) != nil {
			return
		}
		for _, tok := range splitTokens(mockReasoningText) {
			if !sleepCtx(s.cfg.TokenInterval, done) {
				return
			}
			if emit("response.reasoning_summary_text.delta", map[string]any{
				"item_id":       rsID,
				"output_index":  outputIndex,
				"summary_index": 0,
				"delta":         tok,
			}) != nil {
				return
			}
		}
		if emit("response.output_item.done", map[string]any{
			"output_index": outputIndex,
			"item": map[string]any{
				"id":   rsID,
				"type": "reasoning",
				"summary": []any{
					map[string]any{"type": "summary_text", "text": mockReasoningText},
				},
			},
		}) != nil {
			return
		}
		outputIndex++
	}

	msgID := respID + "-msg"
	if emit("response.output_item.added", map[string]any{
		"output_index": outputIndex,
		"item": map[string]any{
			"id": msgID, "type": "message", "status": "in_progress",
			"role": "assistant", "content": []any{},
		},
	}) != nil {
		return
	}
	if emit("response.content_part.added", map[string]any{
		"item_id":       msgID,
		"output_index":  outputIndex,
		"content_index": 0,
		"part":          map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
	}) != nil {
		return
	}

	for _, tok := range splitTokens(reply) {
		if !sleepCtx(s.cfg.TokenInterval, done) {
			return
		}
		if emit("response.output_text.delta", map[string]any{
			"item_id":       msgID,
			"output_index":  outputIndex,
			"content_index": 0,
			"delta":         tok,
		}) != nil {
			return
		}
	}

	if emit("response.output_text.done", map[string]any{
		"item_id":       msgID,
		"output_index":  outputIndex,
		"content_index": 0,
		"text":          reply,
	}) != nil {
		return
	}
	if emit("response.content_part.done", map[string]any{
		"item_id":       msgID,
		"output_index":  outputIndex,
		"content_index": 0,
		"part":          map[string]any{"type": "output_text", "text": reply, "annotations": []any{}},
	}) != nil {
		return
	}
	if emit("response.output_item.done", map[string]any{
		"output_index": outputIndex,
		"item": map[string]any{
			"id": msgID, "type": "message", "status": "completed",
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "output_text", "text": reply, "annotations": []any{}},
			},
		},
	}) != nil {
		return
	}

	_ = emit("response.completed", map[string]any{
		"response": s.responsesEnvelope(respID, model, prompt, reply, reasoning, "completed"),
	})
}
