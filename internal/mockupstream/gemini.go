package mockupstream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// gemini.go implements the Gemini native endpoints (doc §2.3):
//   /{version}/models/{model}:generateContent
//   /{version}/models/{model}:streamGenerateContent
//   /{version}/models/{model}:countTokens
// The action is encoded as a ":suffix" on the path and the key arrives via
// ?key=, so we split on ':' rather than relying on path segments. Matching is
// version-agnostic (v1 / v1beta / v1alpha all route here — gateways pick the
// version per model via a VersionSettings table) and also covers the Vertex AI
// path form (/v1/projects/{p}/locations/{l}/publishers/google/models/{model}:{action}),
// since both end in "/models/{model}:{action}".

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
	return geminiHasModality(req, "AUDIO")
}

// geminiWantsImage reports whether the request declares native image output
// via generationConfig.responseModalities containing "IMAGE"（SupportedImagineModels
// 名单命中后网关注入的形态）。
func geminiWantsImage(req map[string]any) bool {
	return geminiHasModality(req, "IMAGE")
}

// geminiHasModality reports whether generationConfig.responseModalities
// contains the given modality (case-insensitive).
func geminiHasModality(req map[string]any, modality string) bool {
	gc, ok := req["generationConfig"].(map[string]any)
	if !ok {
		return false
	}
	mods, ok := gc["responseModalities"].([]any)
	if !ok {
		return false
	}
	for _, m := range mods {
		if s, ok := m.(string); ok && strings.EqualFold(s, modality) {
			return true
		}
	}
	return false
}

// geminiError writes a Google-style error envelope
// ({"error":{"code","message","status"}}), matching what Vertex/Gemini return
// on validation failures.
func geminiError(w http.ResponseWriter, code int, message, status string) {
	writeJSON(w, code, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
			"status":  status,
		},
	})
}

// geminiThinkingFamily reports whether the model belongs to a thinking-capable
// Gemini generation (2.5+/3.x)——真实上游只对这些模型强制 thoughtSignature。
func geminiThinkingFamily(model string) bool {
	lower := strings.ToLower(model)
	return strings.Contains(lower, "gemini-2.5") || strings.Contains(lower, "gemini-3")
}

// strictGeminiCheck mimics Vertex/Gemini request validation (MOCK_STRICT=1)：
// 递归遍历 contents[].parts，functionCall part 缺 thoughtSignature（仅思考系
// 模型）或 functionResponse 携带非空 id 时返回 400 错误消息。网关的
// "思维签名填充" 和 "移除 functionResponse.id" 修补没生效时，这里必然报错。
func strictGeminiCheck(model string, req map[string]any) string {
	contents, ok := req["contents"].([]any)
	if !ok {
		return ""
	}
	checkSignature := geminiThinkingFamily(model)
	for ci, item := range contents {
		c, ok := item.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := c["parts"].([]any)
		if !ok {
			continue
		}
		for pi, pp := range parts {
			p, ok := pp.(map[string]any)
			if !ok {
				continue
			}
			if _, has := p["functionCall"]; has && checkSignature {
				if sig, _ := p["thoughtSignature"].(string); sig == "" {
					return fmt.Sprintf(
						"Function call part at contents[%d].parts[%d] is missing a thought_signature. "+
							"When thinking is enabled, function calls from previous turns must include their thought signatures.",
						ci, pi)
				}
			}
			if fr, ok := p["functionResponse"].(map[string]any); ok {
				if id, _ := fr["id"].(string); id != "" {
					return fmt.Sprintf(
						"Invalid value at contents[%d].parts[%d].function_response.id: "+
							"ID %q does not match any function call in the request.",
						ci, pi, id)
				}
			}
		}
	}
	return ""
}

func (s *Server) handleGemini(w http.ResponseWriter, r *http.Request) {
	model, action := parseGeminiPath(r.URL.Path)
	if model == "" {
		openAIError(w, http.StatusNotFound, "invalid_request_error", "unparseable gemini path", "not_found")
		return
	}
	body, _ := readBody(r)
	req := decodeJSON(body)
	if s.cfg.Strict && (action == "generateContent" || action == "streamGenerateContent") {
		if msg := strictGeminiCheck(model, req); msg != "" {
			geminiError(w, http.StatusBadRequest, msg, "INVALID_ARGUMENT")
			return
		}
	}
	prompt := extractGeminiPrompt(req)
	reply := s.replyText()
	pt, ct, _ := s.usage(prompt, reply)
	wantsImage := geminiWantsImage(req)

	switch action {
	case "countTokens":
		writeJSON(w, http.StatusOK, map[string]any{"totalTokens": pt})
		return
	case "streamGenerateContent":
		// Real Gemini streams newline-delimited JSON by default, but gateways
		// almost always request `?alt=sse` and parse `data:` frames instead.
		s.streamGemini(w, r, model, reply, pt, ct, wantsImage,
			strings.EqualFold(r.URL.Query().Get("alt"), "sse"))
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
		writeJSON(w, http.StatusOK, s.geminiResponse(model, reply, pt, ct, wantsImage))
	}
}

// geminiUsageMetadata builds the usageMetadata block with Gemini's native
// detail fields: cachedContentTokenCount (context caching hits) plus per-
// modality breakdowns in promptTokensDetails / candidatesTokensDetails. 明细
// 值按配置原样返回、不折算进 promptTokenCount/candidatesTokenCount 主计数；
// TEXT 模态恒在，IMAGE/AUDIO 模态仅在配置了对应 token 数时出现（真实 Gemini
// 只为实际出现的模态返回条目）。
func (s *Server) geminiUsageMetadata(pt, ct int) map[string]any {
	promptDetails := []any{
		map[string]any{"modality": "TEXT", "tokenCount": pt},
	}
	if s.cfg.ImageInputTokens > 0 {
		promptDetails = append(promptDetails, map[string]any{"modality": "IMAGE", "tokenCount": s.cfg.ImageInputTokens})
	}
	if s.cfg.AudioInputTokens > 0 {
		promptDetails = append(promptDetails, map[string]any{"modality": "AUDIO", "tokenCount": s.cfg.AudioInputTokens})
	}
	candDetails := []any{
		map[string]any{"modality": "TEXT", "tokenCount": ct},
	}
	if s.cfg.ImageOutputTokens > 0 {
		candDetails = append(candDetails, map[string]any{"modality": "IMAGE", "tokenCount": s.cfg.ImageOutputTokens})
	}
	if s.cfg.AudioOutputTokens > 0 {
		candDetails = append(candDetails, map[string]any{"modality": "AUDIO", "tokenCount": s.cfg.AudioOutputTokens})
	}
	return map[string]any{
		"promptTokenCount":        pt,
		"candidatesTokenCount":    ct,
		"totalTokenCount":         pt + ct,
		"cachedContentTokenCount": s.cfg.CacheReadTokens,
		"promptTokensDetails":     promptDetails,
		"candidatesTokensDetails": candDetails,
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
		"usageMetadata": s.geminiUsageMetadata(pt, 0),
		"modelVersion":  model,
	}
}

// geminiResponse builds a non-streaming generateContent body. withImage 时在
// text part 后追加 inlineData PNG（真实可解码，复用内置测试图）——模拟
// gemini-*-image 系模型显式声明 responseModalities:["TEXT","IMAGE"] 后的原生
// 图像输出。
func (s *Server) geminiResponse(model, text string, pt, ct int, withImage bool) map[string]any {
	parts := []any{map[string]any{"text": text}}
	if withImage {
		parts = append(parts, map[string]any{
			"inlineData": map[string]any{
				"mimeType": "image/png",
				"data":     string(s.assets.pngB64),
			},
		})
	}
	return map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"role":  "model",
					"parts": parts,
				},
				"finishReason": "STOP",
				"index":        0,
			},
		},
		"usageMetadata": s.geminiUsageMetadata(pt, ct),
		"modelVersion":  model,
	}
}

// streamGemini emits one JSON chunk per token. By default Gemini streams
// newline-delimited JSON objects (each a partial GenerateContentResponse); with
// `?alt=sse` (what gateways request) each chunk goes out as an anonymous SSE
// `data:` frame instead — no [DONE] sentinel, the stream just ends.
// withImage 时最后一个 chunk 的 parts 里追加 inlineData PNG。
func (s *Server) streamGemini(w http.ResponseWriter, r *http.Request, model, reply string, pt, ct int, withImage, sse bool) {
	var writeChunk func([]byte) error
	if sse {
		sw, ok := newSSE(w)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		writeChunk = func(b []byte) error { return sw.data(string(b)) }
	} else {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		writeChunk = func(b []byte) error {
			if _, err := fmt.Fprintf(w, "%s\r\n", b); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		}
	}
	done := clientGone(r)

	if !sleepCtx(randomDelay(model, s.cfg.TTFTMin, s.cfg.TTFTMax), done) {
		return
	}

	tokens := splitTokens(reply)
	for i, tok := range tokens {
		if !sleepCtx(s.cfg.TokenInterval, done) {
			return
		}
		parts := []any{map[string]any{"text": tok}}
		chunk := map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"role":  "model",
						"parts": parts,
					},
					"index": 0,
				},
			},
			"modelVersion": model,
		}
		// Attach finishReason + usage (+ the image part) on the final chunk.
		if i == len(tokens)-1 {
			cand := chunk["candidates"].([]any)[0].(map[string]any)
			if withImage {
				parts = append(parts, map[string]any{
					"inlineData": map[string]any{
						"mimeType": "image/png",
						"data":     string(s.assets.pngB64),
					},
				})
				cand["content"].(map[string]any)["parts"] = parts
			}
			cand["finishReason"] = "STOP"
			chunk["usageMetadata"] = s.geminiUsageMetadata(pt, ct)
		}
		b, _ := json.Marshal(chunk)
		if err := writeChunk(b); err != nil {
			return
		}
	}
}
