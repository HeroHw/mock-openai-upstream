package mockupstream

import (
	"math"
	"net/http"
)

// openai_extra.go implements the remaining OpenAI-compatible endpoints:
// embeddings, audio (speech/transcriptions) and models (doc §2.1).

const embeddingDim = 8 // small, deterministic vector dimension for mock output

// handleEmbeddings returns deterministic vectors for each input string. Vectors
// are derived from a hash of the text so the same input always yields the same
// embedding (useful for cache/dedup assertions in the gateway).
func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	body, _ := readBody(r)
	req := decodeJSON(body)
	model := strField(req, "model", "mock-embedding")

	inputs := normalizeInputs(req["input"])
	data := make([]any, 0, len(inputs))
	promptTokens := 0
	for i, text := range inputs {
		promptTokens += estimateTokens(text)
		data = append(data, map[string]any{
			"object":    "embedding",
			"index":     i,
			"embedding": deterministicVector(text),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
		"model":  model,
		"usage": map[string]any{
			"prompt_tokens": promptTokens,
			"total_tokens":  promptTokens,
		},
	})
}

// normalizeInputs coerces the OpenAI `input` field (string or []string) into a
// slice of strings. An empty/missing input yields a single empty entry so the
// response always contains at least one vector.
func normalizeInputs(v any) []string {
	switch in := v.(type) {
	case string:
		return []string{in}
	case []any:
		out := make([]string, 0, len(in))
		for _, e := range in {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return []string{""}
		}
		return out
	default:
		return []string{""}
	}
}

// deterministicVector produces a stable unit-ish vector from text.
func deterministicVector(text string) []float64 {
	h := hashString(text)
	vec := make([]float64, embeddingDim)
	for i := range vec {
		// Spread bits across dimensions, normalize into [-1,1].
		bits := (h >> (uint(i) * 8)) & 0xff
		vec[i] = math.Round((float64(bits)/255*2-1)*1000) / 1000
	}
	return vec
}

// handleAudioSpeech (TTS) returns real playable audio bytes: the built-in
// 440Hz sine WAV (or the MOCK_ASSETS_DIR override).
func (s *Server) handleAudioSpeech(w http.ResponseWriter, r *http.Request) {
	if !sleepCtx(randomDelay("audio-speech", s.cfg.LatencyMin, s.cfg.LatencyMax), clientGone(r)) {
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Length", itoa(len(s.assets.wav)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.assets.wav)
}

// handleAudioTranscription (STT) accepts a multipart upload and returns a
// fixed transcript. We don't parse the audio; we just acknowledge the upload.
func (s *Server) handleAudioTranscription(w http.ResponseWriter, r *http.Request) {
	if !sleepCtx(randomDelay("audio-transcription", s.cfg.LatencyMin, s.cfg.LatencyMax), clientGone(r)) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"text": s.replyText(),
	})
}

// handleModels returns a static model list covering the popular models of each
// provider family the mock emulates (doc §2.1).
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	type entry struct{ id, ownedBy string }
	models := []entry{
		// 语言模型（chat）
		{"gpt-5.5", "openai"},
		{"gpt-5.4", "openai"},
		{"claude-fable-5", "anthropic"},
		{"claude-opus-4-8", "anthropic"},
		{"dj-claude-sonnet-4-6", "anthropic"},
		{"deepseek-v3.1", "deepseek"},
		{"deepseek-chat", "deepseek"},
		{"qwen-turbo-thinking", "alibaba"},
		{"qwen-plus-thinking", "alibaba"},
		{"kimi-k2.7-code", "moonshot"},
		{"glm-5.2", "zhipu"},
		{"glm-5.1", "zhipu"},
		{"doubao-seed-2-0-pro-260215", "bytedance"},
		// 生图模型
		{"gpt-image-2", "openai"},
		{"wan2.6-t2i", "alibaba"},
		{"wan2.6-image", "alibaba"},
		{"doubao-seedream-5-0-260128", "bytedance"},
		// 音频模型
		{"gpt-4o-mini-tts", "openai"},
		{"gemini-3.1-flash-tts-preview", "google"},
		// 视频模型
		{"wan2.7-i2v", "alibaba"},
		{"wan2.7-t2v", "alibaba"},
		{"wan2.7-videoedit", "alibaba"},
		{"happyhorse-1.1-i2v", "alibaba"},
		{"happyhorse-1.1-r2v", "alibaba"},
		{"happyhorse-1.1-t2v", "alibaba"},
		{"MiniMax-Hailuo-2.3", "minimax"},
		// 兜底通用名（既有测试/夹具在用）
		{"mock-model", "mockupstream"},
		{"mock-embedding", "mockupstream"},
		{"mock-image", "mockupstream"},
		{"mock-video", "mockupstream"},
	}
	data := make([]any, 0, len(models))
	for _, m := range models {
		data = append(data, map[string]any{
			"id":       m.id,
			"object":   "model",
			"created":  0,
			"owned_by": m.ownedBy,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
}
