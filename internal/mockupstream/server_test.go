package mockupstream

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestServer() *httptest.Server {
	cfg := defaults()
	cfg.TokenInterval = 0 // fast streaming in tests
	cfg.TTFTMin = 0
	cfg.TTFTMax = 0
	cfg.LatencyMin = 0
	cfg.LatencyMax = 0
	cfg.ImageSyncDelayMin = 0
	cfg.ImageSyncDelayMax = 0
	cfg.VideoSyncDelayMin = 0
	cfg.VideoSyncDelayMax = 0
	return httptest.NewServer(NewServer(cfg).Handler())
}

func postJSON(t *testing.T, url, body string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

func TestChatNonStream(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp, data := postJSON(t, ts.URL+"/v1/chat/completions",
		`{"model":"gpt-mock","messages":[{"role":"user","content":"hi there"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if out["object"] != "chat.completion" {
		t.Fatalf("want chat.completion, got %v", out["object"])
	}
	if out["model"] != "gpt-mock" {
		t.Fatalf("model should be echoed, got %v", out["model"])
	}
	usage, ok := out["usage"].(map[string]any)
	if !ok || usage["total_tokens"] == nil {
		t.Fatalf("missing usage block: %s", data)
	}
}

func TestChatStreamHasDoneAndUsage(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp, data := postJSON(t, ts.URL+"/v1/chat/completions",
		`{"model":"m","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`)
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("want SSE content type, got %s", resp.Header.Get("Content-Type"))
	}
	s := string(data)
	if !strings.Contains(s, "data: [DONE]") {
		t.Fatalf("stream missing [DONE]: %s", s)
	}
	if !strings.Contains(s, `"usage"`) {
		t.Fatalf("stream missing usage tail: %s", s)
	}
	if !strings.Contains(s, "chat.completion.chunk") {
		t.Fatalf("stream missing chunk frames: %s", s)
	}
}

func TestPathCompatNoV1Prefix(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()
	// Channels whose BaseURL omits /v1 hit /chat/completions directly (§2.1 note).
	resp, _ := postJSON(t, ts.URL+"/chat/completions", `{"model":"m","messages":[]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("no-/v1 path should still route, got %d", resp.StatusCode)
	}
}

func TestEmbeddingsDeterministic(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()
	_, d1 := postJSON(t, ts.URL+"/v1/embeddings", `{"model":"e","input":"same text"}`)
	_, d2 := postJSON(t, ts.URL+"/v1/embeddings", `{"model":"e","input":"same text"}`)
	if string(d1) != string(d2) {
		t.Fatal("same embedding input must yield identical output")
	}
}

func TestAnthropicMessages(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()
	resp, data := postJSON(t, ts.URL+"/v1/messages",
		`{"model":"claude","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	var out map[string]any
	json.Unmarshal(data, &out)
	if out["type"] != "message" {
		t.Fatalf("want type=message, got %v", out["type"])
	}
}

func TestAnthropicStreamEvents(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()
	_, data := postJSON(t, ts.URL+"/v1/messages",
		`{"model":"claude","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	s := string(data)
	for _, want := range []string{"event: message_start", "event: content_block_delta", "event: message_stop"} {
		if !strings.Contains(s, want) {
			t.Fatalf("anthropic stream missing %q: %s", want, s)
		}
	}
}

func TestGeminiGenerateAndCount(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	_, data := postJSON(t, ts.URL+"/v1beta/models/gemini-pro:generateContent",
		`{"contents":[{"parts":[{"text":"hi"}]}]}`)
	var out map[string]any
	json.Unmarshal(data, &out)
	if out["candidates"] == nil {
		t.Fatalf("gemini missing candidates: %s", data)
	}

	_, cd := postJSON(t, ts.URL+"/v1beta/models/gemini-pro:countTokens",
		`{"contents":[{"parts":[{"text":"hi there friend"}]}]}`)
	var cout map[string]any
	json.Unmarshal(cd, &cout)
	if cout["totalTokens"] == nil {
		t.Fatalf("countTokens missing totalTokens: %s", cd)
	}
}

func TestSyncImageGeneration(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()
	_, data := postJSON(t, ts.URL+"/v1/images/generations",
		`{"model":"img","prompt":"a cat","n":2,"response_format":"url"}`)
	var out map[string]any
	json.Unmarshal(data, &out)
	arr, ok := out["data"].([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("want data length 2 (n=2), got %v", out["data"])
	}
	first := arr[0].(map[string]any)
	if !strings.Contains(first["url"].(string), "/__assets/mock-image.png") {
		t.Fatalf("url should point at assets endpoint, got %v", first["url"])
	}
}

func TestDashScopeAsyncLifecycle(t *testing.T) {
	// Short durations so the test runs quickly but still exercises PENDING→SUCCEEDED.
	cfg := defaults()
	cfg.ImageDurationMin = 300 * time.Millisecond
	cfg.ImageDurationMax = 300 * time.Millisecond
	ts := httptest.NewServer(NewServer(cfg).Handler())
	defer ts.Close()

	resp, data := postJSON(t, ts.URL+"/api/v1/services/aigc/text2image/image-synthesis",
		`{"model":"wanx","input":{"prompt":"a dog"}}`)
	if resp.StatusCode != 200 {
		t.Fatalf("submit status %d: %s", resp.StatusCode, data)
	}
	var sub map[string]any
	json.Unmarshal(data, &sub)
	output := sub["output"].(map[string]any)
	if output["task_status"] != "PENDING" && output["task_status"] != "RUNNING" {
		t.Fatalf("submit should return PENDING/RUNNING, got %v", output["task_status"])
	}
	taskID := output["task_id"].(string)

	// Immediately: not yet SUCCEEDED.
	_, qd := http.Get(ts.URL + "/api/v1/tasks/" + taskID)
	_ = qd

	// After the duration, polling should report SUCCEEDED with a result URL.
	time.Sleep(400 * time.Millisecond)
	gresp, gdata := mustGet(t, ts.URL+"/api/v1/tasks/"+taskID)
	if gresp.StatusCode != 200 {
		t.Fatalf("query status %d", gresp.StatusCode)
	}
	var q map[string]any
	json.Unmarshal(gdata, &q)
	qout := q["output"].(map[string]any)
	if qout["task_status"] != "SUCCEEDED" {
		t.Fatalf("after duration want SUCCEEDED, got %v: %s", qout["task_status"], gdata)
	}
	if qout["results"] == nil {
		t.Fatalf("succeeded image task missing results: %s", gdata)
	}
}

func mustGet(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

func TestHealthz(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()
	resp, _ := mustGet(t, ts.URL+"/__mock/healthz")
	if resp.StatusCode != 200 {
		t.Fatalf("healthz status %d", resp.StatusCode)
	}
}

func TestEntryAccessLog(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	ts := newTestServer()
	defer ts.Close()

	// POST：日志须含方法、路由、query 和 body；且 body 复用后 handler 仍能
	// 正常解析（model 正确回显）。
	resp, data := postJSON(t, ts.URL+"/v1/chat/completions?debug=1",
		`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	var out map[string]any
	json.Unmarshal(data, &out)
	if out["model"] != "gpt-5.5" {
		t.Fatalf("body must be replayable after logging, got model=%v", out["model"])
	}
	logged := buf.String()
	for _, want := range []string{"--> POST /v1/chat/completions?debug=1", `"model":"gpt-5.5"`} {
		if !strings.Contains(logged, want) {
			t.Fatalf("access log missing %q: %s", want, logged)
		}
	}

	// healthz 不打日志（healthcheck 噪音）。
	buf.Reset()
	mustGet(t, ts.URL+"/__mock/healthz")
	if strings.Contains(buf.String(), "healthz") {
		t.Fatalf("healthz should not be logged: %s", buf.String())
	}
}

func TestAssetsServed(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()
	resp, data := mustGet(t, ts.URL+"/__assets/mock-image.png")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("asset png not served: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if len(data) == 0 {
		t.Fatal("png asset empty")
	}
}
