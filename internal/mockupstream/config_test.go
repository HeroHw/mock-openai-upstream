package mockupstream

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "mock.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

func TestConfigDefaultsWhenNoFile(t *testing.T) {
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.UsageMode != "echo" {
		t.Fatalf("default usage_mode want echo, got %s", cfg.UsageMode)
	}
	if cfg.VideoConcurrency != 2 {
		t.Fatalf("default video_concurrency want 2, got %d", cfg.VideoConcurrency)
	}
}

func TestConfigFileOverridesDefaults(t *testing.T) {
	p := writeTempConfig(t, `{"api_key":"sk-file","video_concurrency":5,"image_sync_delay_s":3}`)
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.APIKey != "sk-file" {
		t.Fatalf("api_key from file want sk-file, got %s", cfg.APIKey)
	}
	if cfg.VideoConcurrency != 5 {
		t.Fatalf("video_concurrency want 5, got %d", cfg.VideoConcurrency)
	}
	if cfg.ImageSyncDelay != 3*time.Second {
		t.Fatalf("image_sync_delay_s want 3s, got %v", cfg.ImageSyncDelay)
	}
	// Unspecified field keeps its default.
	if cfg.ErrorStatus != 500 {
		t.Fatalf("unspecified error_status should stay 500, got %d", cfg.ErrorStatus)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	p := writeTempConfig(t, `{"api_key":"sk-file","error_status":418}`)
	t.Setenv("MOCK_ERROR_STATUS", "503")
	t.Setenv("MOCK_API_KEY", "sk-env")

	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ErrorStatus != 503 {
		t.Fatalf("env should override file error_status, got %d", cfg.ErrorStatus)
	}
	if cfg.APIKey != "sk-env" {
		t.Fatalf("env should override file api_key, got %s", cfg.APIKey)
	}
}

func TestConfigFileMissingIsError(t *testing.T) {
	if _, err := LoadConfig("/no/such/file.json"); err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestConfigFileUnknownKeyIsError(t *testing.T) {
	p := writeTempConfig(t, `{"usage_modee":"echo"}`) // typo'd key
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("expected error for unknown config key")
	}
}

func TestConfigPathFromEnv(t *testing.T) {
	p := writeTempConfig(t, `{"reply_text":"from-env-path"}`)
	t.Setenv("MOCK_CONFIG", p)
	cfg, err := LoadConfig("") // empty path → fall back to MOCK_CONFIG
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ReplyText != "from-env-path" {
		t.Fatalf("reply_text from MOCK_CONFIG file want from-env-path, got %s", cfg.ReplyText)
	}
}
