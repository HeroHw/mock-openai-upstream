package mockupstream

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"time"
)

// config_file.go handles the JSON config-file layer and the environment-
// variable overlay. Pointer fields let us tell "key absent" (leave the prior
// layer's value) apart from "key present with zero value" (override to zero).
//
// Duration-like fields use the same units as their env vars so the two layers
// read identically: *_ms fields are milliseconds, *_s fields are seconds.
type fileConfig struct {
	TTFTMs          *int     `json:"ttft_ms"`           // SSE 首帧前等待（毫秒）
	TokenIntervalMs *int     `json:"token_interval_ms"` // 流式 chunk 间隔（毫秒）
	LatencyMs       *int     `json:"latency_ms"`        // 非流式整体延迟（毫秒）
	ErrorRate       *float64 `json:"error_rate"`        // 错误注入率 0~1
	ErrorStatus     *int     `json:"error_status"`      // 注入错误的 HTTP 状态码
	ReplyText       *string  `json:"reply_text"`        // chat 回包内容
	UsageMode       *string  `json:"usage_mode"`        // token 用量模式 echo|fixed

	ImageSyncDelayS *int     `json:"image_sync_delay_s"` // 同步生图阻塞秒数
	VideoSyncDelayS *int     `json:"video_sync_delay_s"` // 同步生视频阻塞秒数
	SyncJitterS     *int     `json:"sync_jitter_s"`      // 同步延时 ±抖动秒数
	SyncFailRate    *float64 `json:"sync_fail_rate"`     // 同步失败注入率 0~1

	ImageDurationS   *int     `json:"image_duration_s"`  // 异步生图处理时长秒数
	VideoDurationS   *int     `json:"video_duration_s"`  // 异步生视频处理时长秒数
	VideoConcurrency *int     `json:"video_concurrency"` // 视频并发槽位
	TaskJitterS      *int     `json:"task_jitter_s"`     // 任务时长 ±抖动秒数
	TaskFailRate     *float64 `json:"task_fail_rate"`    // 异步任务失败率 0~1

	RequireKey *bool   `json:"require_key"` // 要求非空凭据但不校验具体值
	APIKey     *string `json:"api_key"`     // 固定 Bearer 校验值
}

// applyFile reads a JSON config file and overlays any present fields onto cfg.
func applyFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var fc fileConfig
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields() // typo in a key name is a config error, not silent no-op
	if err := dec.Decode(&fc); err != nil {
		return err
	}

	setMs(&cfg.TTFT, fc.TTFTMs)
	setMs(&cfg.TokenInterval, fc.TokenIntervalMs)
	setMs(&cfg.Latency, fc.LatencyMs)
	setFloat(&cfg.ErrorRate, fc.ErrorRate)
	setInt(&cfg.ErrorStatus, fc.ErrorStatus)
	setStr(&cfg.ReplyText, fc.ReplyText)
	setStr(&cfg.UsageMode, fc.UsageMode)

	setSec(&cfg.ImageSyncDelay, fc.ImageSyncDelayS)
	setSec(&cfg.VideoSyncDelay, fc.VideoSyncDelayS)
	setSec(&cfg.SyncJitter, fc.SyncJitterS)
	setFloat(&cfg.SyncFailRate, fc.SyncFailRate)

	setSec(&cfg.ImageDuration, fc.ImageDurationS)
	setSec(&cfg.VideoDuration, fc.VideoDurationS)
	setInt(&cfg.VideoConcurrency, fc.VideoConcurrency)
	setSec(&cfg.TaskJitter, fc.TaskJitterS)
	setFloat(&cfg.TaskFailRate, fc.TaskFailRate)

	setBool(&cfg.RequireKey, fc.RequireKey)
	setStr(&cfg.APIKey, fc.APIKey)
	return nil
}

// applyEnv overlays environment variables onto cfg. Each var overrides only if
// set, using the current cfg value as its default so it sits above the file.
func applyEnv(cfg *Config) {
	cfg.TTFT = envMS("MOCK_TTFT_MS", cfg.TTFT)
	cfg.TokenInterval = envMS("MOCK_TOKEN_INTERVAL_MS", cfg.TokenInterval)
	cfg.Latency = envMS("MOCK_LATENCY_MS", cfg.Latency)
	cfg.ErrorRate = envFloat("MOCK_ERROR_RATE", cfg.ErrorRate)
	cfg.ErrorStatus = envInt("MOCK_ERROR_STATUS", cfg.ErrorStatus)
	cfg.ReplyText = envStr("MOCK_REPLY_TEXT", cfg.ReplyText)
	cfg.UsageMode = envStr("MOCK_USAGE_MODE", cfg.UsageMode)

	cfg.ImageSyncDelay = envSec("MOCK_IMAGE_SYNC_DELAY_S", cfg.ImageSyncDelay)
	cfg.VideoSyncDelay = envSec("MOCK_VIDEO_SYNC_DELAY_S", cfg.VideoSyncDelay)
	cfg.SyncJitter = envSec("MOCK_SYNC_JITTER_S", cfg.SyncJitter)
	cfg.SyncFailRate = envFloat("MOCK_SYNC_FAIL_RATE", cfg.SyncFailRate)

	cfg.ImageDuration = envSec("MOCK_IMAGE_DURATION_S", cfg.ImageDuration)
	cfg.VideoDuration = envSec("MOCK_VIDEO_DURATION_S", cfg.VideoDuration)
	cfg.VideoConcurrency = envInt("MOCK_VIDEO_CONCURRENCY", cfg.VideoConcurrency)
	cfg.TaskJitter = envSec("MOCK_TASK_JITTER_S", cfg.TaskJitter)
	cfg.TaskFailRate = envFloat("MOCK_TASK_FAIL_RATE", cfg.TaskFailRate)

	cfg.RequireKey = envBool("MOCK_REQUIRE_KEY", cfg.RequireKey)
	cfg.APIKey = envStr("MOCK_API_KEY", cfg.APIKey)
}

// --- file overlay setters: apply only when the pointer is non-nil ---

func setStr(dst *string, v *string) {
	if v != nil {
		*dst = *v
	}
}
func setInt(dst *int, v *int) {
	if v != nil {
		*dst = *v
	}
}
func setFloat(dst *float64, v *float64) {
	if v != nil {
		*dst = *v
	}
}
func setBool(dst *bool, v *bool) {
	if v != nil {
		*dst = *v
	}
}
func setMs(dst *time.Duration, v *int) {
	if v != nil {
		*dst = time.Duration(*v) * time.Millisecond
	}
}
func setSec(dst *time.Duration, v *int) {
	if v != nil {
		*dst = time.Duration(*v) * time.Second
	}
}

// --- env overlay helpers: default to the passed-in (already-resolved) value ---

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v, ok := os.LookupEnv(key); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

// envMS reads an integer count of milliseconds, defaulting to def.
func envMS(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Millisecond
		}
	}
	return def
}

// envSec reads an integer count of seconds, defaulting to def.
func envSec(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return def
}
