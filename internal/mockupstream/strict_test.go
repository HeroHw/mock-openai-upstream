package mockupstream

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// strict_test.go covers the MOCK_STRICT validation layer (Gemini
// thoughtSignature / functionResponse.id, Anthropic max_tokens /
// budget_tokens), the Gemini native image output (responseModalities IMAGE)
// and the custom error shapes of /__mock/behavior.

func newStrictTestServer() *httptest.Server {
	cfg := defaults()
	cfg.TokenInterval = 0
	cfg.TTFTMin = 0
	cfg.TTFTMax = 0
	cfg.LatencyMin = 0
	cfg.LatencyMax = 0
	cfg.Strict = true
	return httptest.NewServer(NewServer(cfg).Handler())
}

// --- Gemini strict: thoughtSignature ---

// TestStrictGeminiMissingThoughtSignature：思考系模型（2.5+/3）历史 functionCall
// part 缺 thoughtSignature → 400 INVALID_ARGUMENT（模拟 Vertex）。网关的
// "思维签名填充" 没生效时必然踩中。
func TestStrictGeminiMissingThoughtSignature(t *testing.T) {
	ts := newStrictTestServer()
	defer ts.Close()

	body := `{"contents":[
		{"role":"user","parts":[{"text":"hi"}]},
		{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{}}}]},
		{"role":"user","parts":[{"functionResponse":{"name":"get_weather","response":{"ok":true}}}]}
	]}`
	resp, data := postJSON(t, ts.URL+"/v1beta/models/gemini-3-pro:generateContent", body)
	if resp.StatusCode != 400 {
		t.Fatalf("want 400, got %d: %s", resp.StatusCode, data)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	errObj, _ := out["error"].(map[string]any)
	if errObj["status"] != "INVALID_ARGUMENT" {
		t.Fatalf("want INVALID_ARGUMENT, got %v", errObj["status"])
	}
	if !strings.Contains(errObj["message"].(string), "thought_signature") {
		t.Fatalf("message should mention thought_signature: %v", errObj["message"])
	}
}

// TestStrictGeminiSignaturePresent：签名已填充（占位值即可）→ 200。
func TestStrictGeminiSignaturePresent(t *testing.T) {
	ts := newStrictTestServer()
	defer ts.Close()

	body := `{"contents":[
		{"role":"model","parts":[{"functionCall":{"name":"f","args":{}},"thoughtSignature":"skip_thought_signature_validator"}]}
	]}`
	resp, data := postJSON(t, ts.URL+"/v1beta/models/gemini-3-pro:generateContent", body)
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, data)
	}
}

// TestStrictGeminiNonThinkingModelSkipsSignature：非思考系模型（1.5 等）不校验
// thoughtSignature，与真实上游一致。
func TestStrictGeminiNonThinkingModelSkipsSignature(t *testing.T) {
	ts := newStrictTestServer()
	defer ts.Close()

	body := `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"f","args":{}}}]}]}`
	resp, data := postJSON(t, ts.URL+"/v1beta/models/gemini-1.5-pro:generateContent", body)
	if resp.StatusCode != 200 {
		t.Fatalf("want 200 for non-thinking model, got %d: %s", resp.StatusCode, data)
	}
}

// --- Gemini strict: functionResponse.id ---

// TestStrictGeminiFunctionResponseID：functionResponse 带非空 id → 400（模拟
// Vertex 的严格 ID 校验）。网关的 "移除 functionResponse.id" 修补没生效时
// 必然踩中。
func TestStrictGeminiFunctionResponseID(t *testing.T) {
	ts := newStrictTestServer()
	defer ts.Close()

	body := `{"contents":[
		{"role":"user","parts":[{"functionResponse":{"id":"call_abc123","name":"f","response":{}}}]}
	]}`
	resp, data := postJSON(t, ts.URL+"/v1beta/models/gemini-1.5-pro:generateContent", body)
	if resp.StatusCode != 400 {
		t.Fatalf("want 400, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "function_response.id") {
		t.Fatalf("message should mention function_response.id: %s", data)
	}
}

// TestStrictOffAcceptsEverything：默认（Strict=false）同样的畸形请求照常 200，
// 保证严格模式不影响既有联调场景。
func TestStrictOffAcceptsEverything(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	body := `{"contents":[
		{"role":"model","parts":[{"functionCall":{"name":"f","args":{}}}]},
		{"role":"user","parts":[{"functionResponse":{"id":"call_x","name":"f","response":{}}}]}
	]}`
	resp, data := postJSON(t, ts.URL+"/v1beta/models/gemini-3-pro:generateContent", body)
	if resp.StatusCode != 200 {
		t.Fatalf("want 200 with strict off, got %d: %s", resp.StatusCode, data)
	}
}

// --- Anthropic strict ---

// TestStrictAnthropicMissingMaxTokens：缺 max_tokens → 400 "Field required"。
// 网关的 "缺省 MaxTokens 补全" 没生效时必然踩中。
func TestStrictAnthropicMissingMaxTokens(t *testing.T) {
	ts := newStrictTestServer()
	defer ts.Close()

	resp, data := postJSON(t, ts.URL+"/v1/messages",
		`{"model":"claude-fable-5","messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 400 {
		t.Fatalf("want 400, got %d: %s", resp.StatusCode, data)
	}
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	if out["type"] != "error" {
		t.Fatalf("want anthropic error envelope, got %s", data)
	}
	errObj, _ := out["error"].(map[string]any)
	if errObj["type"] != "invalid_request_error" {
		t.Fatalf("want invalid_request_error, got %v", errObj["type"])
	}
	if !strings.Contains(errObj["message"].(string), "max_tokens") {
		t.Fatalf("message should mention max_tokens: %v", errObj["message"])
	}
}

// TestStrictAnthropicBudgetGEMaxTokens：budget_tokens >= max_tokens → 400。
// 网关 BudgetTokens 百分比配成 >=1 的效果由此可见。
func TestStrictAnthropicBudgetGEMaxTokens(t *testing.T) {
	ts := newStrictTestServer()
	defer ts.Close()

	resp, data := postJSON(t, ts.URL+"/v1/messages",
		`{"model":"claude-fable-5","max_tokens":2000,"temperature":1,
		  "thinking":{"type":"enabled","budget_tokens":2000},
		  "messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 400 {
		t.Fatalf("want 400, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "must be greater than") {
		t.Fatalf("message should explain budget constraint: %s", data)
	}
}

// TestStrictAnthropicBudgetTooSmall：budget_tokens < 1024 → 400。
func TestStrictAnthropicBudgetTooSmall(t *testing.T) {
	ts := newStrictTestServer()
	defer ts.Close()

	resp, data := postJSON(t, ts.URL+"/v1/messages",
		`{"model":"claude-fable-5","max_tokens":2000,"temperature":1,
		  "thinking":{"type":"enabled","budget_tokens":512},
		  "messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 400 {
		t.Fatalf("want 400, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "1024") {
		t.Fatalf("message should mention the 1024 floor: %s", data)
	}
}

// TestStrictAnthropicThinkingTemperature：thinking 开启且 temperature != 1 →
// 400（网关 -thinking 适配的 "强制 temperature=1.0" 改写点）。
func TestStrictAnthropicThinkingTemperature(t *testing.T) {
	ts := newStrictTestServer()
	defer ts.Close()

	resp, data := postJSON(t, ts.URL+"/v1/messages",
		`{"model":"claude-fable-5","max_tokens":4096,"temperature":0.7,
		  "thinking":{"type":"enabled","budget_tokens":2048},
		  "messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 400 {
		t.Fatalf("want 400, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "temperature") {
		t.Fatalf("message should mention temperature: %s", data)
	}
}

// TestStrictAnthropicValidThinkingRequest：完整合法的 -thinking 适配产物
// （max_tokens ≥ budget、temperature=1）→ 200 且带 thinking 块。
func TestStrictAnthropicValidThinkingRequest(t *testing.T) {
	ts := newStrictTestServer()
	defer ts.Close()

	resp, data := postJSON(t, ts.URL+"/v1/messages",
		`{"model":"claude-fable-5","max_tokens":10000,"temperature":1,
		  "thinking":{"type":"enabled","budget_tokens":8000},
		  "messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), `"thinking"`) {
		t.Fatalf("response should carry a thinking block: %s", data)
	}
}

// --- Gemini native image output ---

// TestGeminiImageOutput：responseModalities 含 IMAGE 时（SupportedImagineModels
// 名单命中后网关注入的形态），candidates parts 里多一个 inlineData PNG，
// 且 base64 可解码回真实 PNG（魔数校验）。
func TestGeminiImageOutput(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	body := `{"contents":[{"role":"user","parts":[{"text":"draw a cat"}]}],
	          "generationConfig":{"responseModalities":["TEXT","IMAGE"],"imageConfig":{"aspectRatio":"1:1"}}}`
	resp, data := postJSON(t, ts.URL+"/v1beta/models/gemini-2.5-flash-image:generateContent", body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []map[string]any `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	var b64 string
	for _, p := range out.Candidates[0].Content.Parts {
		if inline, ok := p["inlineData"].(map[string]any); ok {
			if inline["mimeType"] != "image/png" {
				t.Fatalf("want image/png, got %v", inline["mimeType"])
			}
			b64, _ = inline["data"].(string)
		}
	}
	if b64 == "" {
		t.Fatalf("no inlineData part in response: %s", data)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("inlineData not valid base64: %v", err)
	}
	if len(raw) < 8 || string(raw[1:4]) != "PNG" {
		t.Fatalf("decoded data is not a PNG")
	}
}

// TestGeminiImageOutputStream：流式最后一个 chunk 带 inlineData part。
func TestGeminiImageOutputStream(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	body := `{"contents":[{"role":"user","parts":[{"text":"draw"}]}],
	          "generationConfig":{"responseModalities":["TEXT","IMAGE"]}}`
	resp, data := postJSON(t, ts.URL+"/v1beta/models/gemini-2.5-flash-image:streamGenerateContent", body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if !strings.Contains(string(data), `"inlineData"`) {
		t.Fatalf("stream should carry inlineData in the final chunk")
	}
}

// TestGeminiNoImageWithoutModality：未声明 IMAGE 模态时不注入 inlineData——
// 验证 "必须显式声明 responseModalities" 的反例分支。
func TestGeminiNoImageWithoutModality(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp, data := postJSON(t, ts.URL+"/v1beta/models/gemini-2.5-flash-image:generateContent",
		`{"contents":[{"role":"user","parts":[{"text":"draw"}]}]}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if strings.Contains(string(data), `"inlineData"`) {
		t.Fatalf("inlineData must not appear without IMAGE modality: %s", data)
	}
}

// --- /__mock/behavior custom error shapes ---

// TestBehaviorCustomErrorCode：error_type/error_code 覆盖 OpenAI 信封字段——
// 网关按错误码识别上游违规（如 Grok CSAM 罚金）时用。
func TestBehaviorCustomErrorCode(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	_, _ = doJSON(t, "POST", ts.URL+"/__mock/behavior",
		`{"status":400,"message":"content violates usage policy","times":1,
		  "error_type":"invalid_request_error","error_code":"content_policy_violation"}`)

	resp, data := postJSON(t, ts.URL+"/v1/chat/completions",
		`{"model":"grok-4","messages":[{"role":"user","content":"x"}]}`)
	if resp.StatusCode != 400 {
		t.Fatalf("want 400, got %d: %s", resp.StatusCode, data)
	}
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	errObj, _ := out["error"].(map[string]any)
	if errObj["code"] != "content_policy_violation" {
		t.Fatalf("want custom code, got %v", errObj["code"])
	}
	if errObj["type"] != "invalid_request_error" {
		t.Fatalf("want custom type, got %v", errObj["type"])
	}

	// 规则耗尽后恢复正常。
	resp2, _ := postJSON(t, ts.URL+"/v1/chat/completions",
		`{"model":"grok-4","messages":[{"role":"user","content":"x"}]}`)
	if resp2.StatusCode != 200 {
		t.Fatalf("rule should be exhausted, got %d", resp2.StatusCode)
	}
}

// TestBehaviorRawBody：raw_body 原样返回——模拟任意上游私有错误信封
// （xAI 违规标记等 OpenAI 信封装不下的形态）。
func TestBehaviorRawBody(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	raw := `{"code":"The system has detected potential CSAM content","error":"blocked"}`
	_, _ = doJSON(t, "POST", ts.URL+"/__mock/behavior",
		`{"status":403,"times":1,"raw_body":`+mustQuote(raw)+`}`)

	resp, data := postJSON(t, ts.URL+"/v1/chat/completions",
		`{"model":"grok-4","messages":[{"role":"user","content":"x"}]}`)
	if resp.StatusCode != 403 {
		t.Fatalf("want 403, got %d", resp.StatusCode)
	}
	if strings.TrimSpace(string(data)) != raw {
		t.Fatalf("raw body should be returned verbatim, got %s", data)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("default content type should be application/json, got %s", ct)
	}
}

// mustQuote JSON-escapes s into a quoted string literal.
func mustQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
