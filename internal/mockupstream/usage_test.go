package mockupstream

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// usage_test.go covers the simulated usage detail fields (doc §5): cache
// read/creation (5m/1h TTL split) and image/audio input/output tokens, each
// surfaced through the owning protocol's native usage shape.

// newUsageTestServer starts a server with every usage detail knob set to a
// distinct value so assertions can tell the fields apart.
func newUsageTestServer() *httptest.Server {
	cfg := defaults()
	cfg.TokenInterval = 0
	cfg.TTFTMin = 0
	cfg.TTFTMax = 0
	cfg.LatencyMin = 0
	cfg.LatencyMax = 0
	cfg.CacheReadTokens = 100
	cfg.CacheCreation5mTokens = 30
	cfg.CacheCreation1hTokens = 7
	cfg.ImageInputTokens = 11
	cfg.ImageOutputTokens = 12
	cfg.AudioInputTokens = 13
	cfg.AudioOutputTokens = 14
	return httptest.NewServer(NewServer(cfg).Handler())
}

func TestAnthropicUsageCacheCreationSplit(t *testing.T) {
	ts := newUsageTestServer()
	defer ts.Close()

	_, data := postJSON(t, ts.URL+"/v1/messages",
		`{"model":"claude-fable-5","messages":[{"role":"user","content":"hi"}]}`)
	var out map[string]any
	json.Unmarshal(data, &out)
	usage := out["usage"].(map[string]any)

	if got := usage["cache_read_input_tokens"].(float64); got != 100 {
		t.Fatalf("cache_read_input_tokens want 100, got %v", got)
	}
	// 总量 = 5m + 1h 之和，与真实 API 语义一致。
	if got := usage["cache_creation_input_tokens"].(float64); got != 37 {
		t.Fatalf("cache_creation_input_tokens want 37, got %v", got)
	}
	cc := usage["cache_creation"].(map[string]any)
	if got := cc["ephemeral_5m_input_tokens"].(float64); got != 30 {
		t.Fatalf("ephemeral_5m_input_tokens want 30, got %v", got)
	}
	if got := cc["ephemeral_1h_input_tokens"].(float64); got != 7 {
		t.Fatalf("ephemeral_1h_input_tokens want 7, got %v", got)
	}
}

func TestAnthropicUsageLegacyCacheCreationField(t *testing.T) {
	// 只设旧字段 cache_creation_tokens：充当 5m 档，总量随之。
	cfg := defaults()
	cfg.TTFTMin, cfg.TTFTMax, cfg.LatencyMin, cfg.LatencyMax = 0, 0, 0, 0
	cfg.CacheCreationTokens = 25
	ts := httptest.NewServer(NewServer(cfg).Handler())
	defer ts.Close()

	_, data := postJSON(t, ts.URL+"/v1/messages",
		`{"model":"claude-fable-5","messages":[{"role":"user","content":"hi"}]}`)
	var out map[string]any
	json.Unmarshal(data, &out)
	usage := out["usage"].(map[string]any)
	if got := usage["cache_creation_input_tokens"].(float64); got != 25 {
		t.Fatalf("legacy total want 25, got %v", got)
	}
	cc := usage["cache_creation"].(map[string]any)
	if got := cc["ephemeral_5m_input_tokens"].(float64); got != 25 {
		t.Fatalf("legacy value should map to 5m bucket, got %v", got)
	}
	if got := cc["ephemeral_1h_input_tokens"].(float64); got != 0 {
		t.Fatalf("1h bucket want 0, got %v", got)
	}
}

func TestOpenAIUsageDetails(t *testing.T) {
	ts := newUsageTestServer()
	defer ts.Close()

	_, data := postJSON(t, ts.URL+"/v1/chat/completions",
		`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
	var out map[string]any
	json.Unmarshal(data, &out)
	usage := out["usage"].(map[string]any)

	pd := usage["prompt_tokens_details"].(map[string]any)
	if got := pd["cached_tokens"].(float64); got != 100 {
		t.Fatalf("cached_tokens want 100, got %v", got)
	}
	if got := pd["audio_tokens"].(float64); got != 13 {
		t.Fatalf("prompt audio_tokens want 13, got %v", got)
	}
	cd := usage["completion_tokens_details"].(map[string]any)
	if got := cd["audio_tokens"].(float64); got != 14 {
		t.Fatalf("completion audio_tokens want 14, got %v", got)
	}
}

func TestZhipuUsageDetails(t *testing.T) {
	ts := newUsageTestServer()
	defer ts.Close()

	_, data := postJSON(t, ts.URL+"/api/paas/v4/chat/completions",
		`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`)
	var out map[string]any
	json.Unmarshal(data, &out)
	usage := out["usage"].(map[string]any)
	pd := usage["prompt_tokens_details"].(map[string]any)
	if got := pd["cached_tokens"].(float64); got != 100 {
		t.Fatalf("zhipu cached_tokens want 100, got %v", got)
	}
}

func TestResponsesUsageCachedTokens(t *testing.T) {
	ts := newUsageTestServer()
	defer ts.Close()

	_, data := postJSON(t, ts.URL+"/v1/responses",
		`{"model":"gpt-5.5","input":"hi"}`)
	var out map[string]any
	json.Unmarshal(data, &out)
	usage := out["usage"].(map[string]any)
	itd := usage["input_tokens_details"].(map[string]any)
	if got := itd["cached_tokens"].(float64); got != 100 {
		t.Fatalf("responses cached_tokens want 100, got %v", got)
	}
}

func TestGeminiUsageDetails(t *testing.T) {
	ts := newUsageTestServer()
	defer ts.Close()

	_, data := postJSON(t, ts.URL+"/v1beta/models/gemini-pro:generateContent",
		`{"contents":[{"parts":[{"text":"hi"}]}]}`)
	var out map[string]any
	json.Unmarshal(data, &out)
	um := out["usageMetadata"].(map[string]any)

	if got := um["cachedContentTokenCount"].(float64); got != 100 {
		t.Fatalf("cachedContentTokenCount want 100, got %v", got)
	}
	wantModality := func(details []any, modality string, tokens float64) {
		t.Helper()
		for _, di := range details {
			d := di.(map[string]any)
			if d["modality"] == modality {
				if got := d["tokenCount"].(float64); got != tokens {
					t.Fatalf("%s tokenCount want %v, got %v", modality, tokens, got)
				}
				return
			}
		}
		t.Fatalf("missing %s modality in %v", modality, details)
	}
	wantModality(um["promptTokensDetails"].([]any), "IMAGE", 11)
	wantModality(um["promptTokensDetails"].([]any), "AUDIO", 13)
	wantModality(um["candidatesTokensDetails"].([]any), "IMAGE", 12)
	wantModality(um["candidatesTokensDetails"].([]any), "AUDIO", 14)
}

func TestGeminiUsageOmitsUnconfiguredModalities(t *testing.T) {
	ts := newTestServer() // all detail knobs zero
	defer ts.Close()

	_, data := postJSON(t, ts.URL+"/v1beta/models/gemini-pro:generateContent",
		`{"contents":[{"parts":[{"text":"hi"}]}]}`)
	var out map[string]any
	json.Unmarshal(data, &out)
	um := out["usageMetadata"].(map[string]any)
	for _, key := range []string{"promptTokensDetails", "candidatesTokensDetails"} {
		details := um[key].([]any)
		if len(details) != 1 || details[0].(map[string]any)["modality"] != "TEXT" {
			t.Fatalf("%s should carry only TEXT when unconfigured: %v", key, details)
		}
	}
}
