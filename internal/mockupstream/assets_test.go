package mockupstream

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assets_test.go verifies the built-in media are REAL decodable data (not
// structural stubs) and that MOCK_ASSETS_DIR overrides them per file.

func TestBuiltinPNGIsDecodable(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp, data := mustGet(t, ts.URL+"/__assets/mock-image.png")
	if resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("content type: %s", resp.Header.Get("Content-Type"))
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("built-in png must decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 512 || b.Dy() != 512 {
		t.Fatalf("want 512x512 test card, got %v", b)
	}
}

func TestBuiltinWAVIsPlayable(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp, data := mustGet(t, ts.URL+"/__assets/mock-audio.wav")
	if resp.Header.Get("Content-Type") != "audio/wav" {
		t.Fatalf("content type: %s", resp.Header.Get("Content-Type"))
	}
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		t.Fatalf("not a RIFF/WAVE file (len=%d)", len(data))
	}
	// 2s @ 24kHz 16-bit mono = 96000 data bytes + 44 header.
	if len(data) != 44+96000 {
		t.Fatalf("unexpected wav size %d", len(data))
	}
}

func TestBuiltinMP4IsRealVideo(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp, data := mustGet(t, ts.URL+"/__assets/mock-video.mp4")
	if resp.Header.Get("Content-Type") != "video/mp4" {
		t.Fatalf("content type: %s", resp.Header.Get("Content-Type"))
	}
	// The embedded Big Buck Bunny clip is ~1MB; the old stub was 44 bytes.
	if len(data) < 100_000 {
		t.Fatalf("mp4 too small to be real video: %d bytes", len(data))
	}
	if !bytes.Contains(data[:64], []byte("ftyp")) {
		t.Fatalf("mp4 missing ftyp box header")
	}
}

func TestAudioSpeechReturnsWAV(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	resp, data := postJSON(t, ts.URL+"/v1/audio/speech",
		`{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy"}`)
	if resp.Header.Get("Content-Type") != "audio/wav" {
		t.Fatalf("content type: %s", resp.Header.Get("Content-Type"))
	}
	if string(data[0:4]) != "RIFF" {
		t.Fatal("speech endpoint should return the playable WAV")
	}
}

func TestB64JSONReturnsRealDecodableImage(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	_, data := postJSON(t, ts.URL+"/v1/images/generations",
		`{"model":"gpt-image-2","prompt":"a cat","response_format":"b64_json"}`)
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	b64 := out["data"].([]any)[0].(map[string]any)["b64_json"].(string)
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("b64_json must be valid base64: %v", err)
	}
	if _, err := png.Decode(bytes.NewReader(raw)); err != nil {
		t.Fatalf("b64_json must decode to a real PNG: %v", err)
	}
}

func TestGeminiTTSAudioIsRealWAV(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	_, data := postJSON(t, ts.URL+"/v1beta/models/gemini-3.1-flash-tts-preview:generateContent",
		`{"contents":[{"parts":[{"text":"say hi"}]}]}`)
	var out map[string]any
	json.Unmarshal(data, &out)
	part := out["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)[0].(map[string]any)
	inline := part["inlineData"].(map[string]any)
	raw, err := base64.StdEncoding.DecodeString(inline["data"].(string))
	if err != nil {
		t.Fatalf("inlineData must be valid base64: %v", err)
	}
	if string(raw[0:4]) != "RIFF" {
		t.Fatal("gemini tts inlineData should be the playable WAV")
	}
}

func TestAssetsDirOverride(t *testing.T) {
	dir := t.TempDir()
	custom := []byte("custom-image-bytes-not-a-real-png")
	if err := os.WriteFile(filepath.Join(dir, "mock-image.png"), custom, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := defaults()
	cfg.AssetsDir = dir
	ts := httptest.NewServer(NewServer(cfg).Handler())
	defer ts.Close()

	// 覆盖的图片按原样返回。
	_, data := mustGet(t, ts.URL+"/__assets/mock-image.png")
	if !bytes.Equal(data, custom) {
		t.Fatalf("override png not served: got %d bytes", len(data))
	}

	// 未覆盖的视频回退到内置 Big Buck Bunny。
	_, vdata := mustGet(t, ts.URL+"/__assets/mock-video.mp4")
	if len(vdata) < 100_000 {
		t.Fatalf("video should fall back to built-in, got %d bytes", len(vdata))
	}

	// b64_json 流跟随覆盖后的图片。
	_, idata := mustGet(t, ts.URL+"/__assets/mock-image.png")
	if !strings.Contains(string(idata), "custom-image-bytes") {
		t.Fatal("b64 source should follow the override")
	}
}
