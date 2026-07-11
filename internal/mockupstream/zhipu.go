package mockupstream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// zhipu.go implements the Zhipu GLM (open.bigmodel.cn) native endpoints under
// /api/paas/v4. Chat and image share OpenAI's response shape but carry Zhipu's
// own id/request_id conventions; video generation (CogVideoX) is async with a
// distinct envelope — submit returns task_status=PROCESSING and the result is
// polled at /api/paas/v4/async-result/{id} instead of DashScope's task path.

// Zhipu async task statuses, distinct from DashScope's PENDING/SUCCEEDED set.
const (
	zhipuStatusProcessing = "PROCESSING"
	zhipuStatusSuccess    = "SUCCESS"
	zhipuStatusFail       = "FAIL"
)

// zhipuTaskStatus maps the internal time-based task state onto Zhipu's status
// strings. PENDING and RUNNING both surface as PROCESSING to the client.
func zhipuTaskStatus(internal string) string {
	switch internal {
	case statusSucceeded:
		return zhipuStatusSuccess
	case statusFailed:
		return zhipuStatusFail
	default:
		return zhipuStatusProcessing
	}
}

// zhipuID formats a Zhipu-style response/request id.
func zhipuID(n uint64) string {
	return fmt.Sprintf("zhipu-mock-%d", n)
}

func (s *Server) handleZhipuChat(w http.ResponseWriter, r *http.Request) {
	body, _ := readBody(r)
	req := decodeJSON(body)
	model := strField(req, "model", "glm-4")

	// Deterministic error injection keyed by model + request sequence.
	n := nextSeq()
	if shouldInject(fmt.Sprintf("%s#%d", model, n), s.cfg.ErrorRate) {
		openAIError(w, s.cfg.ErrorStatus, "server_error", "mock injected failure", "internal_error")
		return
	}

	prompt := extractPrompt(req)
	reply := s.replyText()
	// glm-5.x 系列支持 thinking 参数（{"thinking":{"type":"enabled"}}），
	// 开启后回包携带 reasoning_content。
	reasoning := wantsReasoning(model, req)

	if boolField(req, "stream", false) {
		s.streamZhipuChat(w, r, model, prompt, reply, includeUsage(req), reasoning)
		return
	}

	// Non-streaming: apply overall latency then write a single completion.
	latency := randomDelay(fmt.Sprintf("%s#%d", model, n), s.cfg.LatencyMin, s.cfg.LatencyMax)
	if !sleepCtx(latency, clientGone(r)) {
		return // client disconnected
	}
	pt, ct, tt := s.usage(prompt, reply)
	id := zhipuID(n)
	message := map[string]any{"role": "assistant", "content": reply}
	if reasoning {
		message["reasoning_content"] = mockReasoningText
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         id,
		"request_id": id,
		"created":    0,
		"model":      model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       message,
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     pt,
			"completion_tokens": ct,
			"total_tokens":      tt,
		},
	})
}

// streamZhipuChat emits GLM streaming chunks. Zhipu's SSE stream is byte-for-byte
// the OpenAI chat.completion.chunk shape, so we reuse chunkJSON; only the id
// convention differs. With thinking enabled, reasoning_content deltas precede
// the content deltas (glm-5.x).
func (s *Server) streamZhipuChat(w http.ResponseWriter, r *http.Request, model, prompt, reply string, wantUsage, reasoning bool) {
	sse, ok := newSSE(w)
	if !ok {
		openAIError(w, http.StatusInternalServerError, "server_error", "streaming unsupported", "internal_error")
		return
	}
	done := clientGone(r)
	id := zhipuID(nextSeq())

	// TTFT: wait before the very first frame.
	ttft := randomDelay(id, s.cfg.TTFTMin, s.cfg.TTFTMax)
	if !sleepCtx(ttft, done) {
		return
	}

	// Initial role delta.
	if sse.data(chunkJSON(id, model, map[string]any{"role": "assistant"}, nil)) != nil {
		return
	}

	// Thinking phase before content, mirroring the real glm-5.x stream.
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

	// Optional usage frame in the stream tail.
	if wantUsage {
		pt, ct, tt := s.usage(prompt, reply)
		payload, _ := json.Marshal(map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": 0,
			"model":   model,
			"choices": []any{},
			"usage": map[string]any{
				"prompt_tokens":     pt,
				"completion_tokens": ct,
				"total_tokens":      tt,
			},
		})
		if sse.data(string(payload)) != nil {
			return
		}
	}

	_ = sse.done()
}

// handleZhipuImage serves Zhipu synchronous image generation. The response body
// ({created, data:[{url|b64_json}]}) matches OpenAI's, so we reuse the shared
// sync-media flow.
func (s *Server) handleZhipuImage(w http.ResponseWriter, r *http.Request) {
	s.serveSyncMedia(w, r, s.cfg.ImageSyncDelayMin, s.cfg.ImageSyncDelayMax, "mock-image.png")
}

// handleZhipuVideoSubmit enqueues a CogVideoX job and returns immediately with
// task_status=PROCESSING. The returned id is later polled at
// /api/paas/v4/async-result/{id}.
func (s *Server) handleZhipuVideoSubmit(w http.ResponseWriter, r *http.Request) {
	body, _ := readBody(r)
	req := decodeJSON(body)
	model := strField(req, "model", "cogvideox")

	task := s.queue.Submit(taskKindVideo, model, time.Now())

	writeJSON(w, http.StatusOK, map[string]any{
		"request_id":  fmt.Sprintf("mock-req-%d", nextSeq()),
		"id":          task.ID,
		"model":       model,
		"task_status": zhipuStatusProcessing,
	})
}

// handleZhipuAsyncResult reports a CogVideoX job's status by id. The path is
// /api/paas/v4/async-result/{id}; on SUCCESS it returns the video_result array.
func (s *Server) handleZhipuAsyncResult(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path
	if i := strings.Index(id, "/async-result/"); i >= 0 {
		id = id[i+len("/async-result/"):]
	}
	id = strings.Trim(id, "/")

	task := s.queue.Get(id)
	if task == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": map[string]any{
				"code":    "1211",
				"message": "unknown task id: " + id,
			},
		})
		return
	}

	now := time.Now()
	status := zhipuTaskStatus(task.Status(now))
	resp := map[string]any{
		"id":          task.ID,
		"model":       task.Model,
		"request_id":  fmt.Sprintf("mock-req-%d", nextSeq()),
		"task_status": status,
	}
	if status == zhipuStatusSuccess {
		resp["video_result"] = []any{
			map[string]any{
				"url":             s.assetURL(r, "mock-video.mp4"),
				"cover_image_url": s.assetURL(r, "mock-image.png"),
			},
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
