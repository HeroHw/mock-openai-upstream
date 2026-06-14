package mockupstream

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// openai_media.go implements the synchronous OpenAI image/video generation
// handlers (doc §7). The defining behavior: the handler holds the connection
// open for ~60s (configurable, plus deterministic jitter) before writing the
// result in the same response. This exercises the gateway's request timeout
// configuration and, when the delay is set past that timeout, its timeout path.

// mediaRequest captures the fields we care about from an image/video request.
// Bodies may be JSON or multipart; we parse JSON and fall back to form values.
type mediaRequest struct {
	model          string
	prompt         string
	n              int
	responseFormat string // "url" | "b64_json"
}

func parseMediaRequest(r *http.Request) mediaRequest {
	req := mediaRequest{model: "mock-image", n: 1, responseFormat: "url"}

	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err == nil {
			req.model = formStr(r, "model", req.model)
			req.prompt = formStr(r, "prompt", "")
			req.n = formInt(r, "n", 1)
			req.responseFormat = formStr(r, "response_format", "url")
		}
		return req
	}

	body, _ := readBody(r)
	m := decodeJSON(body)
	req.model = strField(m, "model", req.model)
	req.prompt = strField(m, "prompt", "")
	req.n = intField(m, "n", 1)
	req.responseFormat = strField(m, "response_format", "url")
	if req.n < 1 {
		req.n = 1
	}
	return req
}

func formStr(r *http.Request, key, def string) string {
	if v := r.FormValue(key); v != "" {
		return v
	}
	return def
}

func formInt(r *http.Request, key string, def int) int {
	if v := r.FormValue(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func (s *Server) handleImageGeneration(w http.ResponseWriter, r *http.Request) {
	s.serveSyncMedia(w, r, s.cfg.ImageSyncDelayMin, s.cfg.ImageSyncDelayMax, "mock-image.png")
}

func (s *Server) handleVideoGeneration(w http.ResponseWriter, r *http.Request) {
	s.serveSyncMedia(w, r, s.cfg.VideoSyncDelayMin, s.cfg.VideoSyncDelayMax, "mock-video.mp4")
}

// serveSyncMedia is the shared sync flow for image and video: parse, optionally
// inject failure, sleep (random delay in [min, max]) holding the connection, then write the
// result with n entries (doc §7.1–§7.2).
func (s *Server) serveSyncMedia(w http.ResponseWriter, r *http.Request, minDelay, maxDelay time.Duration, asset string) {
	req := parseMediaRequest(r)

	// Deterministic failure injection keyed by prompt (§7.1).
	if shouldInject(req.prompt, s.cfg.SyncFailRate) {
		openAIError(w, http.StatusInternalServerError, "server_error", "mock injected failure", "internal_error")
		return
	}

	// Hold the connection for a random delay determined by the prompt hash.
	delay := randomDelay(req.prompt, minDelay, maxDelay)
	if !sleepCtx(delay, clientGone(r)) {
		return // client/gateway gave up (timeout path under test)
	}

	b64 := req.responseFormat == "b64_json"
	isVideo := strings.HasSuffix(asset, ".mp4")
	data := make([]any, 0, req.n)
	for i := 0; i < req.n; i++ {
		entry := map[string]any{}
		if b64 {
			entry["b64_json"] = assetBase64(asset)
		} else {
			entry["url"] = s.assetURL(r, asset)
		}
		// images echo the (possibly revised) prompt; videos don't.
		if !isVideo && req.prompt != "" {
			entry["revised_prompt"] = req.prompt
		}
		data = append(data, entry)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"created": 0,
		"data":    data,
	})
}

// assetBase64 returns the base64 of a built-in asset for the b64_json format.
func assetBase64(asset string) string {
	if strings.HasSuffix(asset, ".mp4") {
		return mockMP4Base64
	}
	// ~10MB base64 用于压测大响应体(详见 assets.go)。
	return mockBigB64
}
