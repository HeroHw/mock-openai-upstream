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

// handleAudioSpeech (TTS) returns audio bytes. We reuse the placeholder MP4 as
// stand-in audio; the gateway only needs a non-empty audio byte stream.
func (s *Server) handleAudioSpeech(w http.ResponseWriter, r *http.Request) {
	if !sleepCtx(s.cfg.Latency, clientGone(r)) {
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Length", itoa(len(mockMP4)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(mockMP4)
}

// handleAudioTranscription (STT) accepts a multipart upload and returns a
// fixed transcript. We don't parse the audio; we just acknowledge the upload.
func (s *Server) handleAudioTranscription(w http.ResponseWriter, r *http.Request) {
	if !sleepCtx(s.cfg.Latency, clientGone(r)) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"text": s.replyText(),
	})
}

// handleModels returns a small static model list (doc §2.1).
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	ids := []string{"mock-model", "mock-embedding", "mock-image", "mock-video"}
	data := make([]any, 0, len(ids))
	for _, id := range ids {
		data = append(data, map[string]any{
			"id":       id,
			"object":   "model",
			"created":  0,
			"owned_by": "mockupstream",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
}
