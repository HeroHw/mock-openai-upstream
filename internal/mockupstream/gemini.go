package mockupstream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// gemini.go implements the Gemini native endpoints (doc §2.3):
//   /v1beta/models/{model}:generateContent
//   /v1beta/models/{model}:streamGenerateContent
//   /v1beta/models/{model}:countTokens
// The action is encoded as a ":suffix" on the path and the key arrives via
// ?key=, so we split on ':' rather than relying on path segments.

// parseGeminiPath extracts the model name and action from a Gemini path like
// "/v1beta/models/gemini-pro:generateContent".
func parseGeminiPath(path string) (model, action string) {
	idx := strings.LastIndex(path, "/models/")
	if idx < 0 {
		return "", ""
	}
	rest := path[idx+len("/models/"):]
	if colon := strings.LastIndex(rest, ":"); colon >= 0 {
		return rest[:colon], rest[colon+1:]
	}
	return rest, ""
}

// extractGeminiPrompt concatenates text parts from `contents[].parts[].text`.
func extractGeminiPrompt(m map[string]any) string {
	contents, ok := m["contents"].([]any)
	if !ok {
		return ""
	}
	var sb []byte
	for _, ci := range contents {
		c, ok := ci.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := c["parts"].([]any)
		if !ok {
			continue
		}
		for _, pi := range parts {
			if p, ok := pi.(map[string]any); ok {
				if t, ok := p["text"].(string); ok {
					sb = append(sb, t...)
					sb = append(sb, ' ')
				}
			}
		}
	}
	return string(sb)
}

// geminiWantsAudio reports whether the request asks for TTS output
// (gemini-*-tts 系列)：模型名含 "tts"，或 generationConfig.responseModalities
// 包含 "AUDIO"。
func geminiWantsAudio(model string, req map[string]any) bool {
	if strings.Contains(strings.ToLower(model), "tts") {
		return true
	}
	gc, ok := req["generationConfig"].(map[string]any)
	if !ok {
		return false
	}
	mods, ok := gc["responseModalities"].([]any)
	if !ok {
		return false
	}
	for _, m := range mods {
		if s, ok := m.(string); ok && strings.EqualFold(s, "AUDIO") {
			return true
		}
	}
	return false
}

func (s *Server) handleGemini(w http.ResponseWriter, r *http.Request) {
	model, action := parseGeminiPath(r.URL.Path)
	if model == "" {
		openAIError(w, http.StatusNotFound, "invalid_request_error", "unparseable gemini path", "not_found")
		return
	}
	body, _ := readBody(r)
	req := decodeJSON(body)
	prompt := extractGeminiPrompt(req)
	reply := s.replyText()
	pt, ct, _ := s.usage(prompt, reply)

	switch action {
	case "countTokens":
		writeJSON(w, http.StatusOK, map[string]any{"totalTokens": pt})
		return
	case "streamGenerateContent":
		s.streamGemini(w, r, model, reply, pt, ct)
		return
	default: // generateContent (and any unrecognized action) → non-stream
		n := nextSeq()
		key := fmt.Sprintf("%s#%d", model, n)
		if shouldInject(key, s.cfg.ErrorRate) {
			writeJSON(w, s.cfg.ErrorStatus, map[string]any{
				"error": map[string]any{
					"code":    s.cfg.ErrorStatus,
					"message": "mock injected failure",
					"status":  "INTERNAL",
				},
			})
			return
		}
		if !sleepCtx(randomDelay(key, s.cfg.LatencyMin, s.cfg.LatencyMax), clientGone(r)) {
			return
		}
		// TTS（gemini-3.1-flash-tts-preview）：candidates 里的 part 是
		// inlineData 音频而非 text。
		if geminiWantsAudio(model, req) {
			writeJSON(w, http.StatusOK, s.geminiAudioResponse(model, pt))
			return
		}
		writeJSON(w, http.StatusOK, geminiResponse(model, reply, pt, ct))
	}
}

// geminiAudioResponse builds a TTS generateContent body: the single candidate
// part carries the built-in sine-wave WAV (real, playable audio) as base64 via
// inlineData.
func (s *Server) geminiAudioResponse(model string, pt int) map[string]any {
	return map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"role": "model",
					"parts": []any{map[string]any{
						"inlineData": map[string]any{
							"mimeType": "audio/wav",
							"data":     string(s.assets.wavB64),
						},
					}},
				},
				"finishReason": "STOP",
				"index":        0,
			},
		},
		"usageMetadata": map[string]any{
			"promptTokenCount":     pt,
			"candidatesTokenCount": 0,
			"totalTokenCount":      pt,
		},
		"modelVersion": model,
	}
}

// geminiResponse builds a non-streaming generateContent body.
func geminiResponse(model, text string, pt, ct int) map[string]any {
	return map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"role":  "model",
					"parts": []any{map[string]any{"text": text}},
				},
				"finishReason": "STOP",
				"index":        0,
			},
		},
		"usageMetadata": map[string]any{
			"promptTokenCount":     pt,
			"candidatesTokenCount": ct,
			"totalTokenCount":      pt + ct,
		},
		"modelVersion": model,
	}
}

// streamGemini emits one JSON chunk per token. Gemini streams newline-delimited
// JSON objects (each a partial GenerateContentResponse), not `data:` SSE frames.
func (s *Server) streamGemini(w http.ResponseWriter, r *http.Request, model, reply string, pt, ct int) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	done := clientGone(r)

	if !sleepCtx(randomDelay(model, s.cfg.TTFTMin, s.cfg.TTFTMax), done) {
		return
	}

	tokens := splitTokens(reply)
	for i, tok := range tokens {
		if !sleepCtx(s.cfg.TokenInterval, done) {
			return
		}
		chunk := map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"role":  "model",
						"parts": []any{map[string]any{"text": tok}},
					},
					"index": 0,
				},
			},
			"modelVersion": model,
		}
		// Attach finishReason + usage on the final chunk.
		if i == len(tokens)-1 {
			cand := chunk["candidates"].([]any)[0].(map[string]any)
			cand["finishReason"] = "STOP"
			chunk["usageMetadata"] = map[string]any{
				"promptTokenCount":     pt,
				"candidatesTokenCount": ct,
				"totalTokenCount":      pt + ct,
			}
		}
		b, _ := json.Marshal(chunk)
		if _, err := fmt.Fprintf(w, "%s\r\n", b); err != nil {
			return
		}
		flusher.Flush()
	}
}
