package mockupstream

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// inspect.go 为网关验收提供两组内部端点（与 /__mock/* 一样不做鉴权）：
//
//	请求捕获   GET    /__mock/requests   查询最近捕获的入站请求（头、体、摘要）
//	           DELETE /__mock/requests   清空捕获记录
//	错误注入   POST   /__mock/behavior   设定按需错误规则（状态码/消息/次数/路径过滤）
//	           GET    /__mock/behavior   查看当前规则
//	           DELETE /__mock/behavior   清除规则
//
// 请求捕获用于断言网关下发到上游的最终形态——参数覆盖、请求头覆盖/剔除的
// 生效结果，以及透传请求体的字节级一致性（body_sha256）。错误注入用于渠道
// 自动禁用（auto-disable）演练：与 MOCK_ERROR_RATE 的哈希采样不同，这里是
// 确定的"接下来 N 次必失败"，且 message 可携带关键词供 keywords 规则匹配。

// captureCap bounds the in-memory ring so a long soak test cannot grow without
// limit; captureBodyMax bounds the stored copy of each body (the sha256 and
// byte count still cover the full payload).
const (
	captureCap     = 256
	captureBodyMax = 1 << 20 // 1 MiB
)

// capturedRequest is one recorded inbound request as reported by
// GET /__mock/requests. Exactly one of Body / BodyBase64 is set for non-empty
// bodies: UTF-8 text is returned verbatim, binary payloads are base64-encoded.
type capturedRequest struct {
	Seq           uint64              `json:"seq"`
	Time          string              `json:"time"`
	Method        string              `json:"method"`
	Path          string              `json:"path"`
	Query         string              `json:"query,omitempty"`
	Headers       map[string][]string `json:"headers"`
	Body          string              `json:"body,omitempty"`
	BodyBase64    string              `json:"body_base64,omitempty"`
	BodySHA256    string              `json:"body_sha256,omitempty"`
	BodyBytes     int                 `json:"body_bytes"`
	BodyTruncated bool                `json:"body_truncated,omitempty"`
}

// failRule is an on-demand failure injected ahead of normal dispatch.
//
// 错误响应形态三选一（优先级从高到低）：
//  1. raw_body 非空：原样作为响应体返回（content_type 可指定 MIME，默认
//     application/json）——模拟任意上游的私有错误信封（如 xAI 违规标记）。
//  2. error_type / error_code 任一非空：OpenAI 信封，type/code 用给定值
//     （缺省分别回落 server_error / mock_forced_failure）。
//  3. 都为空：与旧版一致的 OpenAI 信封。
type failRule struct {
	Status      int    `json:"status"`                 // HTTP 状态码，默认 500
	Message     string `json:"message"`                // 错误消息（可埋 auto-disable 关键词）
	Times       int    `json:"times"`                  // >0 剩余次数，耗尽自动移除；0 = 不限次，直到 DELETE
	PathSuffix  string `json:"path_suffix,omitempty"`  // 按路径后缀过滤；空 = 所有业务路径
	ErrorType   string `json:"error_type,omitempty"`   // OpenAI 信封 error.type 覆盖值
	ErrorCode   string `json:"error_code,omitempty"`   // OpenAI 信封 error.code 覆盖值（网关按错误码匹配的场景，如 Grok 违规）
	RawBody     string `json:"raw_body,omitempty"`     // 原样响应体，设置后忽略 message/error_type/error_code
	ContentType string `json:"content_type,omitempty"` // raw_body 的 Content-Type，默认 application/json
	Hits        int    `json:"hits"`                   // 已命中次数（只读）
}

// inspector owns the capture ring and the active failure rule. All access is
// mutex-guarded; handlers and middleware run on arbitrary goroutines.
type inspector struct {
	mu   sync.Mutex
	seq  uint64
	ring []capturedRequest
	rule *failRule
}

func newInspector() *inspector { return &inspector{} }

// record stores one inbound business request. body is the full (re-buffered)
// payload; only a capped copy is retained but the digest covers every byte.
func (in *inspector) record(r *http.Request, body []byte) {
	entry := capturedRequest{
		Time:      time.Now().Format(time.RFC3339Nano),
		Method:    r.Method,
		Path:      r.URL.Path,
		Query:     r.URL.RawQuery,
		Headers:   cloneHeaders(r.Header),
		BodyBytes: len(body),
	}
	if len(body) > 0 {
		sum := sha256.Sum256(body)
		entry.BodySHA256 = hex.EncodeToString(sum[:])
		stored := body
		if len(stored) > captureBodyMax {
			stored = stored[:captureBodyMax]
			entry.BodyTruncated = true
		}
		if utf8.Valid(stored) && !strings.ContainsRune(string(stored), 0) {
			entry.Body = string(stored)
		} else {
			entry.BodyBase64 = base64.StdEncoding.EncodeToString(stored)
		}
	}

	in.mu.Lock()
	defer in.mu.Unlock()
	in.seq++
	entry.Seq = in.seq
	in.ring = append(in.ring, entry)
	if len(in.ring) > captureCap {
		in.ring = in.ring[len(in.ring)-captureCap:]
	}
}

// list returns up to limit captured requests, newest first, optionally
// filtered by path suffix.
func (in *inspector) list(limit int, pathSuffix string) []capturedRequest {
	in.mu.Lock()
	defer in.mu.Unlock()
	out := make([]capturedRequest, 0, limit)
	for i := len(in.ring) - 1; i >= 0 && len(out) < limit; i-- {
		if pathSuffix != "" && !strings.HasSuffix(in.ring[i].Path, pathSuffix) {
			continue
		}
		out = append(out, in.ring[i])
	}
	return out
}

func (in *inspector) clearRequests() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.ring = nil
}

// shouldFail reports whether the active rule fires for path, consuming one
// charge when it does. An exhausted counted rule removes itself. The returned
// rule is a copy, safe to use after the lock is released.
func (in *inspector) shouldFail(path string) (rule failRule, ok bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	r := in.rule
	if r == nil {
		return failRule{}, false
	}
	if r.PathSuffix != "" && !strings.HasSuffix(path, r.PathSuffix) {
		return failRule{}, false
	}
	r.Hits++
	if r.Times > 0 {
		r.Times--
		if r.Times == 0 {
			in.rule = nil
		}
	}
	return *r, true
}

// writeFailResponse renders an injected failure per the rule's shape: raw_body
// verbatim when set, otherwise an OpenAI envelope with optional type/code
// overrides.
func writeFailResponse(w http.ResponseWriter, r failRule) {
	if r.RawBody != "" {
		ct := r.ContentType
		if ct == "" {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(r.Status)
		_, _ = w.Write([]byte(r.RawBody))
		return
	}
	errType, errCode := r.ErrorType, r.ErrorCode
	if errType == "" {
		errType = "server_error"
	}
	if errCode == "" {
		errCode = "mock_forced_failure"
	}
	openAIError(w, r.Status, errType, r.Message, errCode)
}

func (in *inspector) setRule(r failRule) {
	in.mu.Lock()
	defer in.mu.Unlock()
	r.Hits = 0
	in.rule = &r
}

func (in *inspector) getRule() *failRule {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.rule == nil {
		return nil
	}
	cp := *in.rule
	return &cp
}

func (in *inspector) clearRule() {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.rule = nil
}

// cloneHeaders deep-copies an http.Header so the captured entry stays stable
// after the handler mutates or reuses the request.
func cloneHeaders(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, v := range h {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// handleMockRequests serves GET/DELETE /__mock/requests.
func (s *Server) handleMockRequests(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit := 20
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = min(n, captureCap)
			}
		}
		reqs := s.insp.list(limit, r.URL.Query().Get("path_suffix"))
		writeJSON(w, http.StatusOK, map[string]any{
			"count":    len(reqs),
			"requests": reqs,
		})
	case http.MethodDelete:
		s.insp.clearRequests()
		writeJSON(w, http.StatusOK, map[string]any{"status": "cleared"})
	default:
		openAIError(w, http.StatusMethodNotAllowed, "invalid_request_error",
			"use GET to list or DELETE to clear", "method_not_allowed")
	}
}

// handleMockBehavior serves POST/GET/DELETE /__mock/behavior.
func (s *Server) handleMockBehavior(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		body, _ := readBody(r)
		var rule failRule
		if err := json.Unmarshal(body, &rule); err != nil {
			openAIError(w, http.StatusBadRequest, "invalid_request_error",
				"invalid JSON: "+err.Error(), "invalid_body")
			return
		}
		if rule.Status == 0 {
			rule.Status = http.StatusInternalServerError
		}
		if rule.Status < 100 || rule.Status > 599 {
			openAIError(w, http.StatusBadRequest, "invalid_request_error",
				"status must be a valid HTTP status code (100-599)", "invalid_status")
			return
		}
		if rule.Times < 0 {
			openAIError(w, http.StatusBadRequest, "invalid_request_error",
				"times must be >= 0 (0 = unlimited until DELETE)", "invalid_times")
			return
		}
		if rule.Message == "" && rule.RawBody == "" {
			rule.Message = "mock forced failure"
		}
		s.insp.setRule(rule)
		writeJSON(w, http.StatusOK, map[string]any{"status": "set", "rule": rule})
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"rule": s.insp.getRule()})
	case http.MethodDelete:
		s.insp.clearRule()
		writeJSON(w, http.StatusOK, map[string]any{"status": "cleared"})
	default:
		openAIError(w, http.StatusMethodNotAllowed, "invalid_request_error",
			"use POST to set, GET to view, DELETE to clear", "method_not_allowed")
	}
}
