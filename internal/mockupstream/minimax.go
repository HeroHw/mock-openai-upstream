package mockupstream

import (
	"fmt"
	"net/http"
	"time"
)

// minimax.go implements the MiniMax Hailuo async video endpoints
// (MiniMax-Hailuo-2.3 等):
//   POST /v1/video_generation           提交任务，立即返回 task_id
//   GET  /v1/query/video_generation?task_id={id}   轮询状态
//   GET  /v1/files/retrieve?file_id={id}           成功后取下载地址
// 复用 DashScope 同一套时间状态机（TaskQueue），只是换成 MiniMax 的信封：
// 状态字串为 Queueing/Processing/Success/Fail，成功后先给 file_id，再经
// files/retrieve 换取 download_url。

// MiniMax task statuses.
const (
	minimaxStatusQueueing   = "Queueing"
	minimaxStatusProcessing = "Processing"
	minimaxStatusSuccess    = "Success"
	minimaxStatusFail       = "Fail"
)

// minimaxTaskStatus maps the internal time-based task state onto MiniMax's
// status strings.
func minimaxTaskStatus(internal string) string {
	switch internal {
	case statusPending:
		return minimaxStatusQueueing
	case statusSucceeded:
		return minimaxStatusSuccess
	case statusFailed:
		return minimaxStatusFail
	default:
		return minimaxStatusProcessing
	}
}

// minimaxBaseResp builds the base_resp envelope carried by every MiniMax
// response. status_code 0 means success.
func minimaxBaseResp(code int, msg string) map[string]any {
	return map[string]any{
		"status_code": code,
		"status_msg":  msg,
	}
}

// handleMiniMaxVideoSubmit enqueues a Hailuo video job and returns task_id
// immediately, mirroring the async submit flow.
func (s *Server) handleMiniMaxVideoSubmit(w http.ResponseWriter, r *http.Request) {
	body, _ := readBody(r)
	req := decodeJSON(body)
	model := strField(req, "model", "MiniMax-Hailuo-2.3")

	task := s.queue.Submit(taskKindVideo, model, time.Now())

	writeJSON(w, http.StatusOK, map[string]any{
		"task_id":   task.ID,
		"base_resp": minimaxBaseResp(0, "success"),
	})
}

// handleMiniMaxVideoQuery reports a Hailuo job's status via
// ?task_id=. On Success it carries the file_id to feed files/retrieve.
func (s *Server) handleMiniMaxVideoQuery(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("task_id")
	task := s.queue.Get(id)
	if task == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"task_id":   id,
			"status":    minimaxStatusFail,
			"base_resp": minimaxBaseResp(2013, "unknown task_id: "+id),
		})
		return
	}

	status := minimaxTaskStatus(task.Status(time.Now()))
	resp := map[string]any{
		"task_id":   task.ID,
		"status":    status,
		"base_resp": minimaxBaseResp(0, "success"),
	}
	if status == minimaxStatusSuccess {
		resp["file_id"] = minimaxFileID(task.ID)
	}
	writeJSON(w, http.StatusOK, resp)
}

// minimaxFileID derives a stable file id from a task id.
func minimaxFileID(taskID string) string {
	return fmt.Sprintf("%d", hashString(taskID)%1_000_000_000_000)
}

// handleMiniMaxFileRetrieve exchanges a file_id for a download_url pointing at
// the built-in placeholder video. Any non-empty file_id is accepted — the mock
// does not track files separately from tasks.
func (s *Server) handleMiniMaxFileRetrieve(w http.ResponseWriter, r *http.Request) {
	fileID := r.URL.Query().Get("file_id")
	if fileID == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"base_resp": minimaxBaseResp(2013, "missing file_id"),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"file": map[string]any{
			"file_id":      fileID,
			"bytes":        len(mockMP4),
			"filename":     "mock-video.mp4",
			"download_url": s.assetURL(r, "mock-video.mp4"),
		},
		"base_resp": minimaxBaseResp(0, "success"),
	})
}
