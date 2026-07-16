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
	TTFTMinMs       *int     `json:"ttft_min_ms"`       // SSE 首帧前等待最小值（毫秒）
	TTFTMaxMs       *int     `json:"ttft_max_ms"`       // SSE 首帧前等待最大值（毫秒）
	TokenIntervalMs *int     `json:"token_interval_ms"` // 流式 chunk 间隔（毫秒）
	LatencyMinMs    *int     `json:"latency_min_ms"`    // 非流式整体延迟最小值（毫秒）
	LatencyMaxMs    *int     `json:"latency_max_ms"`    // 非流式整体延迟最大值（毫秒）
	ErrorRate       *float64 `json:"error_rate"`        // 错误注入率 0~1
	ErrorStatus     *int     `json:"error_status"`      // 注入错误的 HTTP 状态码
	ReplyText       *string  `json:"reply_text"`        // chat 回包内容
	UsageMode       *string  `json:"usage_mode"`        // token 用量模式 echo|fixed

	CacheReadTokens       *int `json:"cache_read_tokens"`        // 命中缓存读取的 token 数
	CacheCreationTokens   *int `json:"cache_creation_tokens"`    // 写入缓存的 token 数（旧字段，充当 5m 档兜底）
	CacheCreation5mTokens *int `json:"cache_creation_5m_tokens"` // 5 分钟 TTL 缓存创建 token 数
	CacheCreation1hTokens *int `json:"cache_creation_1h_tokens"` // 1 小时 TTL 缓存创建 token 数
	ImageInputTokens      *int `json:"image_input_tokens"`       // 图片输入 token 数
	ImageOutputTokens     *int `json:"image_output_tokens"`      // 图片输出 token 数
	AudioInputTokens      *int `json:"audio_input_tokens"`       // 音频输入 token 数
	AudioOutputTokens     *int `json:"audio_output_tokens"`      // 音频输出 token 数

	ImageSyncDelayMinS *int     `json:"image_sync_delay_min_s"` // 同步生图最小延迟秒数
	ImageSyncDelayMaxS *int     `json:"image_sync_delay_max_s"` // 同步生图最大延迟秒数
	VideoSyncDelayMinS *int     `json:"video_sync_delay_min_s"` // 同步生视频最小延迟秒数
	VideoSyncDelayMaxS *int     `json:"video_sync_delay_max_s"` // 同步生视频最大延迟秒数
	SyncFailRate       *float64 `json:"sync_fail_rate"`         // 同步失败注入率 0~1

	ImageDurationMinS *int     `json:"image_duration_min_s"` // 异步生图最小处理时长秒数
	ImageDurationMaxS *int     `json:"image_duration_max_s"` // 异步生图最大处理时长秒数
	VideoDurationMinS *int     `json:"video_duration_min_s"` // 异步生视频最小处理时长秒数
	VideoDurationMaxS *int     `json:"video_duration_max_s"` // 异步生视频最大处理时长秒数
	VideoConcurrency  *int     `json:"video_concurrency"`    // 视频并发槽位
	TaskFailRate      *float64 `json:"task_fail_rate"`       // 异步任务失败率 0~1

	RequireKey *bool   `json:"require_key"` // 要求非空凭据但不校验具体值
	APIKey     *string `json:"api_key"`     // 固定 Bearer 校验值
	AssetsDir  *string `json:"assets_dir"`  // 真实素材目录（mock-image.png / mock-video.mp4 / mock-audio.wav）
	Strict     *bool   `json:"strict"`      // 严格校验模式：模拟真上游对畸形请求报 400
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

	setMs(&cfg.TTFTMin, fc.TTFTMinMs)
	setMs(&cfg.TTFTMax, fc.TTFTMaxMs)
	setMs(&cfg.TokenInterval, fc.TokenIntervalMs)
	setMs(&cfg.LatencyMin, fc.LatencyMinMs)
	setMs(&cfg.LatencyMax, fc.LatencyMaxMs)
	setFloat(&cfg.ErrorRate, fc.ErrorRate)
	setInt(&cfg.ErrorStatus, fc.ErrorStatus)
	setStr(&cfg.ReplyText, fc.ReplyText)
	setStr(&cfg.UsageMode, fc.UsageMode)

	setInt(&cfg.CacheReadTokens, fc.CacheReadTokens)
	setInt(&cfg.CacheCreationTokens, fc.CacheCreationTokens)
	setInt(&cfg.CacheCreation5mTokens, fc.CacheCreation5mTokens)
	setInt(&cfg.CacheCreation1hTokens, fc.CacheCreation1hTokens)
	setInt(&cfg.ImageInputTokens, fc.ImageInputTokens)
	setInt(&cfg.ImageOutputTokens, fc.ImageOutputTokens)
	setInt(&cfg.AudioInputTokens, fc.AudioInputTokens)
	setInt(&cfg.AudioOutputTokens, fc.AudioOutputTokens)

	setSec(&cfg.ImageSyncDelayMin, fc.ImageSyncDelayMinS)
	setSec(&cfg.ImageSyncDelayMax, fc.ImageSyncDelayMaxS)
	setSec(&cfg.VideoSyncDelayMin, fc.VideoSyncDelayMinS)
	setSec(&cfg.VideoSyncDelayMax, fc.VideoSyncDelayMaxS)
	setFloat(&cfg.SyncFailRate, fc.SyncFailRate)

	setSec(&cfg.ImageDurationMin, fc.ImageDurationMinS)
	setSec(&cfg.ImageDurationMax, fc.ImageDurationMaxS)
	setSec(&cfg.VideoDurationMin, fc.VideoDurationMinS)
	setSec(&cfg.VideoDurationMax, fc.VideoDurationMaxS)
	setInt(&cfg.VideoConcurrency, fc.VideoConcurrency)
	setFloat(&cfg.TaskFailRate, fc.TaskFailRate)

	setBool(&cfg.RequireKey, fc.RequireKey)
	setStr(&cfg.APIKey, fc.APIKey)
	setStr(&cfg.AssetsDir, fc.AssetsDir)
	setBool(&cfg.Strict, fc.Strict)
	return nil
}

// applyEnv overlays environment variables onto cfg. Each var overrides only if
// set, using the current cfg value as its default so it sits above the file.
func applyEnv(cfg *Config) {
	cfg.TTFTMin = envMS("MOCK_TTFT_MIN_MS", cfg.TTFTMin)
	cfg.TTFTMax = envMS("MOCK_TTFT_MAX_MS", cfg.TTFTMax)
	cfg.TokenInterval = envMS("MOCK_TOKEN_INTERVAL_MS", cfg.TokenInterval)
	cfg.LatencyMin = envMS("MOCK_LATENCY_MIN_MS", cfg.LatencyMin)
	cfg.LatencyMax = envMS("MOCK_LATENCY_MAX_MS", cfg.LatencyMax)
	cfg.ErrorRate = envFloat("MOCK_ERROR_RATE", cfg.ErrorRate)
	cfg.ErrorStatus = envInt("MOCK_ERROR_STATUS", cfg.ErrorStatus)
	cfg.ReplyText = envStr("MOCK_REPLY_TEXT", cfg.ReplyText)
	cfg.UsageMode = envStr("MOCK_USAGE_MODE", cfg.UsageMode)

	cfg.CacheReadTokens = envInt("MOCK_CACHE_READ_TOKENS", cfg.CacheReadTokens)
	cfg.CacheCreationTokens = envInt("MOCK_CACHE_CREATION_TOKENS", cfg.CacheCreationTokens)
	cfg.CacheCreation5mTokens = envInt("MOCK_CACHE_CREATION_5M_TOKENS", cfg.CacheCreation5mTokens)
	cfg.CacheCreation1hTokens = envInt("MOCK_CACHE_CREATION_1H_TOKENS", cfg.CacheCreation1hTokens)
	cfg.ImageInputTokens = envInt("MOCK_IMAGE_INPUT_TOKENS", cfg.ImageInputTokens)
	cfg.ImageOutputTokens = envInt("MOCK_IMAGE_OUTPUT_TOKENS", cfg.ImageOutputTokens)
	cfg.AudioInputTokens = envInt("MOCK_AUDIO_INPUT_TOKENS", cfg.AudioInputTokens)
	cfg.AudioOutputTokens = envInt("MOCK_AUDIO_OUTPUT_TOKENS", cfg.AudioOutputTokens)

	cfg.ImageSyncDelayMin = envSec("MOCK_IMAGE_SYNC_DELAY_MIN_S", cfg.ImageSyncDelayMin)
	cfg.ImageSyncDelayMax = envSec("MOCK_IMAGE_SYNC_DELAY_MAX_S", cfg.ImageSyncDelayMax)
	cfg.VideoSyncDelayMin = envSec("MOCK_VIDEO_SYNC_DELAY_MIN_S", cfg.VideoSyncDelayMin)
	cfg.VideoSyncDelayMax = envSec("MOCK_VIDEO_SYNC_DELAY_MAX_S", cfg.VideoSyncDelayMax)
	cfg.SyncFailRate = envFloat("MOCK_SYNC_FAIL_RATE", cfg.SyncFailRate)

	cfg.ImageDurationMin = envSec("MOCK_IMAGE_DURATION_MIN_S", cfg.ImageDurationMin)
	cfg.ImageDurationMax = envSec("MOCK_IMAGE_DURATION_MAX_S", cfg.ImageDurationMax)
	cfg.VideoDurationMin = envSec("MOCK_VIDEO_DURATION_MIN_S", cfg.VideoDurationMin)
	cfg.VideoDurationMax = envSec("MOCK_VIDEO_DURATION_MAX_S", cfg.VideoDurationMax)
	cfg.VideoConcurrency = envInt("MOCK_VIDEO_CONCURRENCY", cfg.VideoConcurrency)
	cfg.TaskFailRate = envFloat("MOCK_TASK_FAIL_RATE", cfg.TaskFailRate)

	cfg.RequireKey = envBool("MOCK_REQUIRE_KEY", cfg.RequireKey)
	cfg.APIKey = envStr("MOCK_API_KEY", cfg.APIKey)
	cfg.AssetsDir = envStr("MOCK_ASSETS_DIR", cfg.AssetsDir)
	cfg.Strict = envBool("MOCK_STRICT", cfg.Strict)
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
