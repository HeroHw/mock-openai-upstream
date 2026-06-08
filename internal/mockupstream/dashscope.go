package mockupstream

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// dashscope.go implements the Alibaba DashScope async image/video endpoints
// (doc §8). Submit returns immediately with a task_id + PENDING; polling
// /api/v1/tasks/{id} returns the time-derived status until SUCCEEDED/FAILED.

// handleDashScopeSubmit enqueues a task and returns task_id + PENDING at once,
// without blocking the connection (doc §8.4).
func (s *Server) handleDashScopeSubmit(w http.ResponseWriter, r *http.Request, kind string) {
	body, _ := readBody(r)
	req := decodeJSON(body)
	model := strField(req, "model", "mock-"+kind)

	task := s.queue.Submit(kind, model, time.Now())

	writeJSON(w, http.StatusOK, map[string]any{
		"request_id": fmt.Sprintf("mock-req-%d", nextSeq()),
		"output": map[string]any{
			"task_id":     task.ID,
			"task_status": statusPending,
		},
	})
}

// handleTaskQuery returns the current status of a task by ID. The path is fixed
// at /api/v1/tasks/{id} and only reuses the BaseURL scheme/host, not the submit
// service path (doc §8 note).
func (s *Server) handleTaskQuery(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/")
	id = strings.Trim(id, "/")

	task := s.queue.Get(id)
	if task == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"request_id": fmt.Sprintf("mock-req-%d", nextSeq()),
			"code":       "InvalidParameter",
			"message":    "unknown task_id: " + id,
		})
		return
	}

	now := time.Now()
	status := task.Status(now)
	output := map[string]any{
		"task_id":     task.ID,
		"task_status": status,
	}
	resp := map[string]any{
		"request_id": fmt.Sprintf("mock-req-%d", nextSeq()),
		"output":     output,
	}

	switch status {
	case statusSucceeded:
		s.fillSuccess(r, task, output, resp)
	case statusFailed:
		output["code"] = "InternalError.Timeout"
		output["message"] = "mock injected failure"
	}

	writeJSON(w, http.StatusOK, resp)
}

// fillSuccess populates the result fields for a completed task. Image and video
// use different result shapes (doc §8.4).
func (s *Server) fillSuccess(r *http.Request, task *Task, output, resp map[string]any) {
	if task.Kind == taskKindVideo {
		output["video_url"] = s.assetURL(r, "mock-video.mp4")
		resp["usage"] = map[string]any{"video_count": 1}
		return
	}
	output["results"] = []any{
		map[string]any{"url": s.assetURL(r, "mock-image.png")},
	}
	resp["usage"] = map[string]any{"image_count": 1}
}
