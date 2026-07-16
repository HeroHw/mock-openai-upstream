package mockupstream

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// providers_test.go covers the provider-specific behaviors added for the
// popular-model matrix: reasoning_content (thinking models), Anthropic extended
// thinking blocks, Gemini TTS inlineData audio, the widened DashScope wan2.x
// path family and the MiniMax Hailuo async video flow.

func TestChatReasoningContentByModelName(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	// 模型名含 thinking → 非流式回包携带 reasoning_content。
	_, data := postJSON(t, ts.URL+"/v1/chat/completions",
		`{"model":"qwen-plus-thinking","messages":[{"role":"user","content":"hi"}]}`)
	var out map[string]any
	json.Unmarshal(data, &out)
	msg := out["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if msg["reasoning_content"] == nil {
		t.Fatalf("thinking model missing reasoning_content: %s", data)
	}

	// 普通模型不带 reasoning_content。
	_, data2 := postJSON(t, ts.URL+"/v1/chat/completions",
		`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
	if strings.Contains(string(data2), "reasoning_content") {
		t.Fatalf("non-thinking model should not carry reasoning_content: %s", data2)
	}
}

func TestChatReasoningContentByEnableThinking(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	// enable_thinking=true（Qwen/DashScope 风格）流式：reasoning_content 增量
	// 出现在 content 增量之前。
	_, data := postJSON(t, ts.URL+"/v1/chat/completions",
		`{"model":"deepseek-v3.1","enable_thinking":true,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	s := string(data)
	ri := strings.Index(s, "reasoning_content")
	ci := strings.Index(s, `"content"`)
	if ri < 0 {
		t.Fatalf("stream missing reasoning_content deltas: %s", s)
	}
	if ci >= 0 && ri > ci {
		t.Fatalf("reasoning deltas must precede content deltas: %s", s)
	}
	if !strings.Contains(s, "data: [DONE]") {
		t.Fatalf("stream missing [DONE]: %s", s)
	}
}

func TestZhipuChatThinkingParam(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	// glm-5.x thinking 参数（豆包/智谱风格 {"thinking":{"type":"enabled"}}）。
	_, data := postJSON(t, ts.URL+"/api/paas/v4/chat/completions",
		`{"model":"glm-5.2","thinking":{"type":"enabled"},"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(string(data), "reasoning_content") {
		t.Fatalf("glm thinking response missing reasoning_content: %s", data)
	}
}

func TestAnthropicExtendedThinking(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	// 非流式：content 数组第一块是 thinking。
	_, data := postJSON(t, ts.URL+"/v1/messages",
		`{"model":"claude-fable-5","thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"hi"}]}`)
	var out map[string]any
	json.Unmarshal(data, &out)
	content := out["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("want [thinking, text] blocks, got %s", data)
	}
	if content[0].(map[string]any)["type"] != "thinking" {
		t.Fatalf("first block should be thinking: %s", data)
	}

	// 流式：thinking_delta 帧和 signature_delta 帧，text 块 index 顺延为 1。
	_, sdata := postJSON(t, ts.URL+"/v1/messages",
		`{"model":"claude-opus-4-8","stream":true,"thinking":{"type":"enabled"},"messages":[{"role":"user","content":"hi"}]}`)
	ss := string(sdata)
	for _, want := range []string{"thinking_delta", "signature_delta", `"index":1`} {
		if !strings.Contains(ss, want) {
			t.Fatalf("anthropic thinking stream missing %q: %s", want, ss)
		}
	}
}

func TestGeminiTTSAudio(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	_, data := postJSON(t, ts.URL+"/v1beta/models/gemini-3.1-flash-tts-preview:generateContent",
		`{"contents":[{"parts":[{"text":"say hi"}]}],"generationConfig":{"responseModalities":["AUDIO"]}}`)
	var out map[string]any
	json.Unmarshal(data, &out)
	cand := out["candidates"].([]any)[0].(map[string]any)
	parts := cand["content"].(map[string]any)["parts"].([]any)
	inline, ok := parts[0].(map[string]any)["inlineData"].(map[string]any)
	if !ok || inline["data"] == "" {
		t.Fatalf("tts response missing inlineData audio: %s", data)
	}
	if !strings.HasPrefix(inline["mimeType"].(string), "audio/") {
		t.Fatalf("tts mimeType should be audio/*, got %v", inline["mimeType"])
	}
}

func TestDashScopeWanPathFamily(t *testing.T) {
	cfg := defaults()
	cfg.ImageDurationMin = 0
	cfg.ImageDurationMax = 0
	cfg.VideoDurationMin = 0
	cfg.VideoDurationMax = 0
	ts := httptest.NewServer(NewServer(cfg).Handler())
	defer ts.Close()

	// wan2.7-i2v 走 image2video/video-synthesis；happyhorse 同理。
	for _, p := range []string{
		"/api/v1/services/aigc/image2video/video-synthesis",
		"/api/v1/services/aigc/video-generation/video-synthesis",
		"/api/v1/services/aigc/image2image/image-synthesis",
		"/api/v1/services/aigc/text2image/image-synthesis",
	} {
		resp, data := postJSON(t, ts.URL+p, `{"model":"wan2.7-i2v","input":{"prompt":"x"}}`)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: status %d: %s", p, resp.StatusCode, data)
		}
		var out map[string]any
		json.Unmarshal(data, &out)
		if out["output"].(map[string]any)["task_id"] == "" {
			t.Fatalf("%s: missing task_id: %s", p, data)
		}
	}
}

func TestMiniMaxVideoLifecycle(t *testing.T) {
	cfg := defaults()
	cfg.VideoDurationMin = 200 * time.Millisecond
	cfg.VideoDurationMax = 200 * time.Millisecond
	ts := httptest.NewServer(NewServer(cfg).Handler())
	defer ts.Close()

	// 提交。
	resp, data := postJSON(t, ts.URL+"/v1/video_generation",
		`{"model":"MiniMax-Hailuo-2.3","prompt":"a cat"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("submit status %d: %s", resp.StatusCode, data)
	}
	var sub map[string]any
	json.Unmarshal(data, &sub)
	taskID, _ := sub["task_id"].(string)
	if taskID == "" {
		t.Fatalf("submit missing task_id: %s", data)
	}
	if sub["base_resp"].(map[string]any)["status_code"].(float64) != 0 {
		t.Fatalf("submit base_resp not ok: %s", data)
	}

	// 完成后轮询 → Success + file_id。
	time.Sleep(300 * time.Millisecond)
	_, qdata := mustGet(t, ts.URL+"/v1/query/video_generation?task_id="+taskID)
	var q map[string]any
	json.Unmarshal(qdata, &q)
	if q["status"] != "Success" {
		t.Fatalf("want Success, got %v: %s", q["status"], qdata)
	}
	fileID, _ := q["file_id"].(string)
	if fileID == "" {
		t.Fatalf("success missing file_id: %s", qdata)
	}

	// files/retrieve → download_url 指向内置视频资产。
	_, fdata := mustGet(t, ts.URL+"/v1/files/retrieve?file_id="+fileID)
	var f map[string]any
	json.Unmarshal(fdata, &f)
	url, _ := f["file"].(map[string]any)["download_url"].(string)
	if !strings.Contains(url, "/__assets/mock-video.mp4") {
		t.Fatalf("download_url should point at assets, got %s", fdata)
	}

	// 未知 task_id → Fail 信封。
	_, ud := mustGet(t, ts.URL+"/v1/query/video_generation?task_id=nope")
	var u map[string]any
	json.Unmarshal(ud, &u)
	if u["status"] != "Fail" {
		t.Fatalf("unknown task should be Fail, got %s", ud)
	}
}

func TestResponsesNonStream(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	// input 为字符串形式；gpt-5.x 带 reasoning 参数 → output 含 reasoning 项。
	resp, data := postJSON(t, ts.URL+"/v1/responses",
		`{"model":"gpt-5.5","reasoning":{"effort":"high"},"input":"hi there"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if out["object"] != "response" || out["status"] != "completed" {
		t.Fatalf("want completed response object, got %s", data)
	}
	output := out["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("want [reasoning, message] output items, got %s", data)
	}
	if output[0].(map[string]any)["type"] != "reasoning" {
		t.Fatalf("first output item should be reasoning: %s", data)
	}
	msg := output[1].(map[string]any)
	text := msg["content"].([]any)[0].(map[string]any)
	if text["type"] != "output_text" || text["text"] == "" {
		t.Fatalf("message missing output_text: %s", data)
	}
	usage := out["usage"].(map[string]any)
	details := usage["output_tokens_details"].(map[string]any)
	if details["reasoning_tokens"].(float64) <= 0 {
		t.Fatalf("reasoning request should report reasoning_tokens: %s", data)
	}

	// 无 reasoning 参数的普通模型 → 只有 message 项。
	_, data2 := postJSON(t, ts.URL+"/v1/responses",
		`{"model":"gpt-5.4","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	var out2 map[string]any
	json.Unmarshal(data2, &out2)
	if n := len(out2["output"].([]any)); n != 1 {
		t.Fatalf("plain request should have single message item, got %d: %s", n, data2)
	}
}

func TestResponsesStreamEvents(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp, data := postJSON(t, ts.URL+"/v1/responses",
		`{"model":"gpt-5.5","stream":true,"reasoning":{"effort":"low"},"input":"hi"}`)
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("want SSE content type, got %s", resp.Header.Get("Content-Type"))
	}
	s := string(data)
	for _, want := range []string{
		"event: response.created",
		"event: response.in_progress",
		"event: response.reasoning_summary_text.delta",
		"event: response.output_item.added",
		"event: response.content_part.added",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.output_item.done",
		"event: response.completed",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("responses stream missing %q: %s", want, s)
		}
	}
	// reasoning 增量必须先于正文增量。
	if strings.Index(s, "reasoning_summary_text.delta") > strings.Index(s, "output_text.delta") {
		t.Fatalf("reasoning deltas must precede text deltas: %s", s)
	}
	// Responses 流以 response.completed 收尾，不使用 [DONE] 哨兵。
	if strings.Contains(s, "data: [DONE]") {
		t.Fatalf("responses stream must not emit [DONE]: %s", s)
	}
}

func TestGPTImageEnvelope(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	// gpt-image 系列返回新版信封：background/output_format/quality/size 顶层
	// 字段 + Responses 风格 usage，data 恒为 b64_json（忽略 response_format）。
	resp, data := postJSON(t, ts.URL+"/v1/images/generations",
		`{"model":"gpt-image-2","prompt":"画一副清明上河图","n":1,"size":"1024x1024","quality":"high","style":"vivid"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, data)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if out["background"] != "opaque" || out["output_format"] != "png" {
		t.Fatalf("missing gpt-image envelope fields: %v", out["background"])
	}
	if out["quality"] != "high" || out["size"] != "1024x1024" {
		t.Fatalf("quality/size should echo the request, got %v/%v", out["quality"], out["size"])
	}
	entry := out["data"].([]any)[0].(map[string]any)
	if entry["b64_json"].(string) == "" {
		t.Fatal("gpt-image data must carry b64_json")
	}
	usage := out["usage"].(map[string]any)
	outDetails := usage["output_tokens_details"].(map[string]any)
	if outDetails["image_tokens"].(float64) <= 0 || outDetails["text_tokens"].(float64) != 0 {
		t.Fatalf("output tokens must be all image_tokens: %s", data)
	}
	inDetails := usage["input_tokens_details"].(map[string]any)
	if inDetails["text_tokens"].(float64) <= 0 {
		t.Fatalf("input tokens must count prompt text: %s", data)
	}
	if usage["total_tokens"].(float64) != usage["input_tokens"].(float64)+usage["output_tokens"].(float64) {
		t.Fatalf("total_tokens mismatch: %s", data)
	}

	// 非 gpt-image 模型仍走经典 DALL·E 形状（默认 url、无信封字段）。
	_, data2 := postJSON(t, ts.URL+"/v1/images/generations",
		`{"model":"dall-e-3","prompt":"a cat"}`)
	var out2 map[string]any
	json.Unmarshal(data2, &out2)
	if _, has := out2["output_format"]; has {
		t.Fatalf("classic models must keep the legacy shape: %s", data2)
	}
	if out2["data"].([]any)[0].(map[string]any)["url"] == "" {
		t.Fatalf("classic shape should default to url: %s", data2)
	}
}

func TestModelsListCoversPopularModels(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()
	_, data := mustGet(t, ts.URL+"/v1/models")
	s := string(data)
	for _, id := range []string{
		"gpt-5.5", "claude-fable-5", "deepseek-v3.1", "qwen-turbo-thinking",
		"kimi-k2.7-code", "glm-5.2", "doubao-seed-2-0-pro-260215",
		"gpt-image-2", "wan2.6-t2i", "doubao-seedream-5-0-260128",
		"gpt-4o-mini-tts", "gemini-3.1-flash-tts-preview",
		"wan2.7-i2v", "happyhorse-1.1-t2v", "MiniMax-Hailuo-2.3",
	} {
		if !strings.Contains(s, `"`+id+`"`) {
			t.Fatalf("models list missing %s", id)
		}
	}
}
