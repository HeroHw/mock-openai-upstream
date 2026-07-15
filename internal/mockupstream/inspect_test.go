package mockupstream

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// inspect_test.go covers the acceptance-support endpoints: request capture
// (/__mock/requests) and on-demand failure injection (/__mock/behavior).

func doJSON(t *testing.T, method, url, body string) (*http.Response, []byte) {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	data := readAll(t, resp)
	return resp, data
}

func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	var buf []byte
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf
}

func listRequests(t *testing.T, base, query string) []map[string]any {
	t.Helper()
	_, data := mustGet(t, base+"/__mock/requests"+query)
	var out struct {
		Requests []map[string]any `json:"requests"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("bad capture json: %v: %s", err, data)
	}
	return out.Requests
}

// TestCaptureRecordsHeadersAndBody：捕获须包含头、体、sha256，且 body 逐字保留
// （网关侧参数覆盖/透传验收依赖这一点）。
func TestCaptureRecordsHeadersAndBody(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	body := `{"model":"m","temperature":0.1,"messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Custom-Header", "hello-acceptance")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("chat status %d", resp.StatusCode)
	}

	reqs := listRequests(t, ts.URL, "?path_suffix=/chat/completions")
	if len(reqs) == 0 {
		t.Fatal("no captured requests")
	}
	got := reqs[0]
	if got["body"] != body {
		t.Fatalf("body must be captured verbatim, got %v", got["body"])
	}
	sum := sha256.Sum256([]byte(body))
	if got["body_sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 mismatch: %v", got["body_sha256"])
	}
	headers, _ := got["headers"].(map[string]any)
	if headers == nil {
		t.Fatalf("missing headers: %v", got)
	}
	hv, _ := headers["X-Custom-Header"].([]any)
	if len(hv) != 1 || hv[0] != "hello-acceptance" {
		t.Fatalf("custom header not captured: %v", headers)
	}
}

// TestCaptureFilterAndClear：path_suffix 过滤 + DELETE 清空。
func TestCaptureFilterAndClear(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	postJSON(t, ts.URL+"/v1/chat/completions", `{"model":"a","messages":[]}`)
	postJSON(t, ts.URL+"/v1/embeddings", `{"model":"e","input":"x"}`)

	if got := listRequests(t, ts.URL, "?path_suffix=/embeddings"); len(got) != 1 {
		t.Fatalf("suffix filter want 1, got %d", len(got))
	}
	// 内部端点不应被捕获。
	for _, r := range listRequests(t, ts.URL, "") {
		if strings.HasPrefix(r["path"].(string), "/__mock/") {
			t.Fatalf("internal endpoint captured: %v", r["path"])
		}
	}

	doJSON(t, http.MethodDelete, ts.URL+"/__mock/requests", "")
	if got := listRequests(t, ts.URL, ""); len(got) != 0 {
		t.Fatalf("after clear want 0, got %d", len(got))
	}
}

// TestBehaviorFailNTimes：times=2 时前两次失败（状态码+消息生效），第三次恢复
// 正常——auto-disable 阈值演练的核心形态。
func TestBehaviorFailNTimes(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp, data := doJSON(t, http.MethodPost, ts.URL+"/__mock/behavior",
		`{"status":429,"message":"rate limit exceeded (mock)","times":2}`)
	if resp.StatusCode != 200 {
		t.Fatalf("set rule status %d: %s", resp.StatusCode, data)
	}

	for i := 0; i < 2; i++ {
		r, d := postJSON(t, ts.URL+"/v1/chat/completions", `{"model":"m","messages":[]}`)
		if r.StatusCode != 429 {
			t.Fatalf("hit %d: want 429, got %d", i+1, r.StatusCode)
		}
		if !strings.Contains(string(d), "rate limit exceeded (mock)") {
			t.Fatalf("hit %d: message missing: %s", i+1, d)
		}
	}
	r, _ := postJSON(t, ts.URL+"/v1/chat/completions", `{"model":"m","messages":[]}`)
	if r.StatusCode != 200 {
		t.Fatalf("rule exhausted, want 200, got %d", r.StatusCode)
	}
	// 耗尽后规则自动移除。
	_, gd := mustGet(t, ts.URL+"/__mock/behavior")
	if !strings.Contains(string(gd), `"rule":null`) {
		t.Fatalf("exhausted rule should be removed: %s", gd)
	}
}

// TestBehaviorPathSuffixAndDelete：规则只对匹配后缀的路径生效；DELETE 即恢复。
func TestBehaviorPathSuffixAndDelete(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	doJSON(t, http.MethodPost, ts.URL+"/__mock/behavior",
		`{"status":503,"times":0,"path_suffix":"/embeddings"}`)

	if r, _ := postJSON(t, ts.URL+"/v1/chat/completions", `{"model":"m","messages":[]}`); r.StatusCode != 200 {
		t.Fatalf("non-matching path should pass, got %d", r.StatusCode)
	}
	if r, _ := postJSON(t, ts.URL+"/v1/embeddings", `{"model":"e","input":"x"}`); r.StatusCode != 503 {
		t.Fatalf("matching path should fail, got %d", r.StatusCode)
	}
	// times=0 不限次，再打一次仍失败。
	if r, _ := postJSON(t, ts.URL+"/v1/embeddings", `{"model":"e","input":"x"}`); r.StatusCode != 503 {
		t.Fatalf("unlimited rule should keep failing, got %d", r.StatusCode)
	}

	doJSON(t, http.MethodDelete, ts.URL+"/__mock/behavior", "")
	if r, _ := postJSON(t, ts.URL+"/v1/embeddings", `{"model":"e","input":"x"}`); r.StatusCode != 200 {
		t.Fatalf("after delete want 200, got %d", r.StatusCode)
	}
}

// TestBehaviorValidation：非法 status / times 拒绝。
func TestBehaviorValidation(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	if r, _ := doJSON(t, http.MethodPost, ts.URL+"/__mock/behavior", `{"status":42}`); r.StatusCode != 400 {
		t.Fatalf("bad status must 400, got %d", r.StatusCode)
	}
	if r, _ := doJSON(t, http.MethodPost, ts.URL+"/__mock/behavior", `{"times":-1}`); r.StatusCode != 400 {
		t.Fatalf("negative times must 400, got %d", r.StatusCode)
	}
	if r, _ := doJSON(t, http.MethodPost, ts.URL+"/__mock/behavior", `not json`); r.StatusCode != 400 {
		t.Fatalf("bad json must 400, got %d", r.StatusCode)
	}
}

// TestBehaviorBeatsAuth：错误注入发生在鉴权之前无所谓——但内部端点必须
// 免疫注入与捕获，healthz 始终可用。
func TestBehaviorInternalImmune(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	doJSON(t, http.MethodPost, ts.URL+"/__mock/behavior", `{"status":500,"times":0}`)
	if r, _ := mustGet(t, ts.URL+"/__mock/healthz"); r.StatusCode != 200 {
		t.Fatalf("healthz must survive injection, got %d", r.StatusCode)
	}
	if r, _ := mustGet(t, ts.URL+"/__mock/behavior"); r.StatusCode != 200 {
		t.Fatalf("behavior view must survive injection, got %d", r.StatusCode)
	}
	doJSON(t, http.MethodDelete, ts.URL+"/__mock/behavior", "")
}

// TestCaptureBodyReplayable：捕获中间件重放 body 后，handler 仍能正常解析。
func TestCaptureBodyReplayable(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp, data := postJSON(t, ts.URL+"/v1/chat/completions",
		`{"model":"replay-check","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var out map[string]any
	json.Unmarshal(data, &out)
	if out["model"] != "replay-check" {
		t.Fatalf("body must remain readable after capture, got model=%v", out["model"])
	}
}
