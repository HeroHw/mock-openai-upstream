package mockupstream

import (
	"crypto/subtle"
	"log"
	"net/http"
	"strings"
)

// Server holds the mock upstream state and routes requests to the right
// protocol handler (doc §3). It carries config and the async task queue; there
// is no DB and no external dependency — everything lives in memory.
type Server struct {
	cfg    Config
	queue  *TaskQueue
	assets *assetStore
}

// NewServer constructs a Server with the given config.
func NewServer(cfg Config) *Server {
	return &Server{
		cfg:    cfg,
		queue:  NewTaskQueue(cfg),
		assets: newAssetStore(cfg.AssetsDir),
	}
}

// Handler builds the http.Handler with all routes registered. We use a single
// dispatcher rather than http.ServeMux because several upstreams omit the `/v1`
// prefix and Gemini encodes the action as a `:suffix` on the path, which a
// plain ServeMux cannot match cleanly (doc §2 note).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Management / asset endpoints (§10, §3).
	mux.HandleFunc("/__assets/", s.handleAssets)
	mux.HandleFunc("/__mock/healthz", s.handleHealthz)

	// Everything else goes through the protocol dispatcher.
	mux.HandleFunc("/", s.dispatch)

	return s.withMiddleware(mux)
}

// withMiddleware wraps the mux with optional API-key checking and access logs.
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	// Key enforcement is on if explicitly requested, or implied by a fixed key.
	enforce := s.cfg.RequireKey || s.cfg.APIKey != ""
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if enforce && !isMockEndpoint(r.URL.Path) {
			if !s.authOK(r) {
				openAIError(w, http.StatusUnauthorized, "invalid_request_error",
					"missing or invalid credentials", "invalid_api_key")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// authOK reports whether the request carries an acceptable credential. When
// MOCK_API_KEY is set, the presented credential must equal it exactly (constant
// -time compare). Otherwise any non-empty credential passes.
func (s *Server) authOK(r *http.Request) bool {
	got := presentedKey(r)
	if got == "" {
		return false
	}
	if s.cfg.APIKey == "" {
		return true // any non-empty credential
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.APIKey)) == 1
}

// presentedKey extracts the credential from the request, accepting the
// OpenAI/Anthropic `Authorization: Bearer <key>` / `x-api-key` headers or
// Gemini's `?key=` query parameter. The "Bearer " prefix is stripped if present.
func presentedKey(r *http.Request) string {
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	if k := strings.TrimSpace(r.Header.Get("x-api-key")); k != "" {
		return k
	}
	return strings.TrimSpace(r.URL.Query().Get("key"))
}

// dispatch routes by normalized path to the correct protocol handler. Paths are
// matched on suffix so the optional `/v1` (or `/v1beta`) prefix is tolerated.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	switch {
	// --- DashScope async image/video (§8). Matching on the bare
	// `/image-synthesis` / `/video-synthesis` suffix (not the full service
	// path) covers every wanx/wan2.x service family: text2image, image2image
	// (wan2.6-image 图生图), video-generation (wan2.7-t2v), image2video
	// (wan2.7-i2v / happyhorse-1.1-i2v·r2v) and video editing. ---
	case strings.HasPrefix(path, "/api/v1/tasks/"):
		s.handleTaskQuery(w, r)
	case strings.HasSuffix(path, "/image-synthesis"):
		s.handleDashScopeSubmit(w, r, taskKindImage)
	case strings.HasSuffix(path, "/video-synthesis"):
		s.handleDashScopeSubmit(w, r, taskKindVideo)

	// --- MiniMax Hailuo async video. `/query/video_generation` must match
	// before the bare `/video_generation` submit suffix. ---
	case strings.HasSuffix(path, "/query/video_generation"):
		s.handleMiniMaxVideoQuery(w, r)
	case strings.HasSuffix(path, "/video_generation"):
		s.handleMiniMaxVideoSubmit(w, r)
	case strings.HasSuffix(path, "/files/retrieve"):
		s.handleMiniMaxFileRetrieve(w, r)

	// --- Gemini native (§2.3): /v1beta/models/{model}:{action} ---
	case strings.Contains(path, "/models/") && strings.Contains(path, ":"):
		s.handleGemini(w, r)

	// --- Anthropic native (§2.2) ---
	case strings.HasSuffix(path, "/messages"):
		s.handleAnthropicMessages(w, r)

	// --- Zhipu GLM native (§9): open.bigmodel.cn /api/paas/v4/... . These are
	// matched before the generic OpenAI-compatible suffixes below so the Zhipu
	// chat/image/video paths route to their own handlers (async video in
	// particular differs from OpenAI's synchronous flow). ---
	case strings.HasPrefix(path, "/api/paas/v4/async-result/"):
		s.handleZhipuAsyncResult(w, r)
	case strings.HasSuffix(path, "/paas/v4/videos/generations"):
		s.handleZhipuVideoSubmit(w, r)
	case strings.HasSuffix(path, "/paas/v4/images/generations"):
		s.handleZhipuImage(w, r)
	case strings.HasSuffix(path, "/paas/v4/chat/completions"):
		s.handleZhipuChat(w, r)

	// --- OpenAI-compatible (§2.1) ---
	case strings.HasSuffix(path, "/chat/completions"):
		s.handleChatCompletions(w, r)
	case strings.HasSuffix(path, "/responses"):
		s.handleResponses(w, r)
	case strings.HasSuffix(path, "/embeddings"):
		s.handleEmbeddings(w, r)
	case strings.HasSuffix(path, "/images/generations"):
		s.handleImageGeneration(w, r)
	case strings.HasSuffix(path, "/images/edits"):
		s.handleImageGeneration(w, r)
	case strings.HasSuffix(path, "/images/variations"):
		s.handleImageGeneration(w, r)
	case strings.HasSuffix(path, "/videos/generations"):
		s.handleVideoGeneration(w, r)
	case strings.HasSuffix(path, "/videos/edits"):
		s.handleVideoGeneration(w, r)
	case strings.HasSuffix(path, "/audio/speech"):
		s.handleAudioSpeech(w, r)
	case strings.HasSuffix(path, "/audio/transcriptions"):
		s.handleAudioTranscription(w, r)
	case strings.HasSuffix(path, "/models"):
		s.handleModels(w, r)

	default:
		openAIError(w, http.StatusNotFound, "invalid_request_error",
			"unknown endpoint: "+path, "not_found")
	}
}

// handleHealthz is a liveness probe for docker-compose healthchecks (§10).
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// isMockEndpoint reports whether a path is an internal management/asset route
// that should bypass API-key enforcement.
func isMockEndpoint(path string) bool {
	return strings.HasPrefix(path, "/__assets/") || strings.HasPrefix(path, "/__mock/")
}

// Logf logs a server-level message with the mock prefix.
func Logf(format string, args ...any) {
	log.Printf("[mockupstream] "+format, args...)
}
