package mockupstream

import (
	"encoding/json"
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
	size           string // gpt-image 信封回显（如 "1024x1024"）
	quality        string // gpt-image 信封回显（如 "high"）
}

func parseMediaRequest(r *http.Request) mediaRequest {
	req := mediaRequest{model: "mock-image", n: 1, responseFormat: "url",
		size: "1024x1024", quality: "high"}

	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err == nil {
			req.model = formStr(r, "model", req.model)
			req.prompt = formStr(r, "prompt", "")
			req.n = formInt(r, "n", 1)
			req.responseFormat = formStr(r, "response_format", "url")
			req.size = formStr(r, "size", req.size)
			req.quality = formStr(r, "quality", req.quality)
		}
		return req
	}

	body, _ := readBody(r)
	m := decodeJSON(body)
	req.model = strField(m, "model", req.model)
	req.prompt = strField(m, "prompt", "")
	req.n = intField(m, "n", 1)
	req.responseFormat = strField(m, "response_format", "url")
	req.size = strField(m, "size", req.size)
	req.quality = strField(m, "quality", req.quality)
	if req.n < 1 {
		req.n = 1
	}
	return req
}

// isGPTImage reports whether the request targets the gpt-image family, which
// uses the newer response envelope (background/output_format/quality/size/
// usage) and always returns b64_json — the real API ignores response_format.
func (m mediaRequest) isGPTImage() bool {
	return strings.HasPrefix(strings.ToLower(m.model), "gpt-image")
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

	isVideo := strings.HasSuffix(asset, ".mp4")
	if !isVideo && req.isGPTImage() {
		s.writeGPTImageJSON(w, req)
		return
	}
	b64 := req.responseFormat == "b64_json"
	s.writeMediaJSON(w, r, req.n, b64, isVideo, req.prompt, asset)
}

// writeMediaJSON streams the sync image/video response. It deliberately avoids
// the generic map + json.Encode path (writeJSON): the base64 payload (a real
// PNG/MP4, potentially megabytes with MOCK_ASSETS_DIR overrides) would be
// buffered and escape-scanned per request, which under concurrency blows up
// memory and CPU. Here the payload is written straight from a shared read-only
// []byte — never copied into a per-request buffer — and only the small,
// possibly-unsafe fields (url, prompt) are JSON-escaped. Content-Length is set
// so load tests don't pay for chunked encoding.
func (s *Server) writeMediaJSON(w http.ResponseWriter, r *http.Request, n int, b64, isVideo bool, prompt, asset string) {
	// The large payload: shared base64 of the real built-in (or overridden)
	// media. base64 text needs no JSON escaping, so it is written verbatim.
	var payload []byte
	if b64 {
		if isVideo {
			payload = s.assets.mp4B64
		} else {
			payload = s.assets.pngB64
		}
	}
	urlQuoted, _ := json.Marshal(s.assetURL(r, asset)) // quoted + escaped
	promptQuoted, _ := json.Marshal(prompt)
	includePrompt := !isVideo && prompt != ""

	// Per-entry segments, written in order: head, [payload], tail.
	// b64:  {"b64_json":"  <payload>  "[,"revised_prompt":<p>]}
	// url:  {"url":<urlQuoted>[,"revised_prompt":<p>]}
	var head, tail []byte
	if b64 {
		head = []byte(`{"b64_json":"`)
		tail = append(tail, '"')
	} else {
		head = append(head, `{"url":`...)
		head = append(head, urlQuoted...)
	}
	if includePrompt {
		tail = append(tail, `,"revised_prompt":`...)
		tail = append(tail, promptQuoted...)
	}
	tail = append(tail, '}')

	prefix := []byte(`{"created":0,"data":[`)
	suffix := []byte(`]}`)

	// Content-Length is exact and known up front (payload is a fixed length).
	entryLen := len(head) + len(payload) + len(tail)
	commas := 0
	if n > 1 {
		commas = n - 1
	}
	total := len(prefix) + n*entryLen + commas + len(suffix)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", itoa(total))
	w.WriteHeader(http.StatusOK)

	w.Write(prefix)
	for i := 0; i < n; i++ {
		if i > 0 {
			w.Write([]byte{','})
		}
		w.Write(head)
		if len(payload) > 0 {
			w.Write(payload) // shared bytes, written by reference — never copied
		}
		w.Write(tail)
	}
	w.Write(suffix)
}

// gpt-image 系列的 usage 常量：真实 API 按图片规格计费 output image_tokens，
// mock 固定为一个有代表性的值（1024x1024/high 档约 7000+），便于计费断言。
const gptImageOutputTokens = 7024

// writeGPTImageJSON streams the gpt-image response envelope, which differs
// from the classic DALL·E shape: top-level background/output_format/quality/
// size fields plus a Responses-style usage block with image/text token
// details. Like writeMediaJSON it writes the shared base64 payload by
// reference and sets an exact Content-Length.
func (s *Server) writeGPTImageJSON(w http.ResponseWriter, req mediaRequest) {
	payload := s.assets.pngB64

	inputTokens := estimateTokens(req.prompt)
	if s.cfg.UsageMode == "fixed" {
		inputTokens = 10
	}
	outputTokens := gptImageOutputTokens
	if s.cfg.ImageOutputTokens > 0 {
		outputTokens = s.cfg.ImageOutputTokens
	}
	usageJSON, _ := json.Marshal(map[string]any{
		"input_tokens": inputTokens,
		"input_tokens_details": map[string]any{
			"image_tokens": 0,
			"text_tokens":  inputTokens,
		},
		"output_tokens": outputTokens,
		"output_tokens_details": map[string]any{
			"image_tokens": outputTokens,
			"text_tokens":  0,
		},
		"total_tokens": inputTokens + outputTokens,
	})
	qualityQuoted, _ := json.Marshal(req.quality)
	sizeQuoted, _ := json.Marshal(req.size)

	prefix := []byte(`{"background":"opaque","created":0,"data":[`)
	head := []byte(`{"b64_json":"`)
	tail := []byte(`"}`)
	var suffix []byte
	suffix = append(suffix, `],"output_format":"png","quality":`...)
	suffix = append(suffix, qualityQuoted...)
	suffix = append(suffix, `,"quota":189711,"size":`...)
	suffix = append(suffix, sizeQuoted...)
	suffix = append(suffix, `,"usage":`...)
	suffix = append(suffix, usageJSON...)
	suffix = append(suffix, '}')

	n := req.n
	entryLen := len(head) + len(payload) + len(tail)
	commas := 0
	if n > 1 {
		commas = n - 1
	}
	total := len(prefix) + n*entryLen + commas + len(suffix)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", itoa(total))
	w.WriteHeader(http.StatusOK)

	w.Write(prefix)
	for i := 0; i < n; i++ {
		if i > 0 {
			w.Write([]byte{','})
		}
		w.Write(head)
		w.Write(payload) // shared bytes, written by reference — never copied
		w.Write(tail)
	}
	w.Write(suffix)
}
