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
