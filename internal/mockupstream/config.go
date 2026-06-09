package mockupstream

import (
	"fmt"
	"os"
	"time"
)

// Config holds all behavior knobs for the mock upstream service.
//
// Values are resolved in three layers, each overriding the previous (doc §4):
//
//	built-in defaults  <  config file (JSON)  <  environment variables
//
// So the service runs with zero configuration, a JSON file can pin a scenario,
// and an env var can still override any single field for a one-off CI run.
// ListenAddr is the fixed address the server listens on. It is intentionally
// not configurable: in a container you remap the host-side port via the
// docker-compose port mapping; for local runs it is always :18080.
const ListenAddr = ":9050"

type Config struct {
	// chat 类通用行为
	TTFTMin       time.Duration // MOCK_TTFT_MIN_MS / ttft_min_ms：SSE 首帧前等待最小值（毫秒）
	TTFTMax       time.Duration // MOCK_TTFT_MAX_MS / ttft_max_ms：SSE 首帧前等待最大值（毫秒）
	TokenInterval time.Duration // MOCK_TOKEN_INTERVAL_MS / token_interval_ms：流式每个 chunk 间隔，控制吐字速率
	LatencyMin    time.Duration // MOCK_LATENCY_MIN_MS / latency_min_ms：非流式整体延迟最小值（毫秒）
	LatencyMax    time.Duration // MOCK_LATENCY_MAX_MS / latency_max_ms：非流式整体延迟最大值（毫秒）
	ErrorRate     float64       // MOCK_ERROR_RATE / error_rate：错误注入率 0~1，按 model+序号哈希确定性命中
	ErrorStatus   int           // MOCK_ERROR_STATUS / error_status：注入错误时返回的 HTTP 状态码
	ReplyText     string        // MOCK_REPLY_TEXT / reply_text：chat 回包内容，可固定便于断言
	UsageMode     string        // MOCK_USAGE_MODE / usage_mode：token 用量模式，"echo"=按输入估算 | "fixed"=固定值

	// 同步生图/生视频（OpenAI 协议，§7）
	ImageSyncDelayMin time.Duration // MOCK_IMAGE_SYNC_DELAY_MIN_S / image_sync_delay_min_s：同步生图最小延迟秒数
	ImageSyncDelayMax time.Duration // MOCK_IMAGE_SYNC_DELAY_MAX_S / image_sync_delay_max_s：同步生图最大延迟秒数
	VideoSyncDelayMin time.Duration // MOCK_VIDEO_SYNC_DELAY_MIN_S / video_sync_delay_min_s：同步生视频最小延迟秒数
	VideoSyncDelayMax time.Duration // MOCK_VIDEO_SYNC_DELAY_MAX_S / video_sync_delay_max_s：同步生视频最大延迟秒数
	SyncFailRate      float64       // MOCK_SYNC_FAIL_RATE / sync_fail_rate：同步失败注入率，按 prompt 哈希确定性命中

	// 异步生图/生视频（DashScope，§8）
	ImageDurationMin time.Duration // MOCK_IMAGE_DURATION_MIN_S / image_duration_min_s：异步生图最小处理时长秒数
	ImageDurationMax time.Duration // MOCK_IMAGE_DURATION_MAX_S / image_duration_max_s：异步生图最大处理时长秒数
	VideoDurationMin time.Duration // MOCK_VIDEO_DURATION_MIN_S / video_duration_min_s：异步生视频最小处理时长秒数
	VideoDurationMax time.Duration // MOCK_VIDEO_DURATION_MAX_S / video_duration_max_s：异步生视频最大处理时长秒数
	VideoConcurrency int           // MOCK_VIDEO_CONCURRENCY / video_concurrency：视频并发槽位，超出的排队为 PENDING
	TaskFailRate     float64       // MOCK_TASK_FAIL_RATE / task_fail_rate：异步任务失败率，按 task_id 哈希命中转 FAILED

	// 可选增强（§10）
	RequireKey bool   // MOCK_REQUIRE_KEY / require_key：要求非空凭据但不校验具体值
	APIKey     string // MOCK_API_KEY / api_key：设置后强制校验，凭据须等于此固定值，否则 401
}

// defaults returns the built-in configuration used when nothing is overridden.
func defaults() Config {
	return Config{
		TTFTMin:       1 * time.Second,
		TTFTMax:       30 * time.Second,
		TokenInterval: 10 * time.Millisecond,
		LatencyMin:    1 * time.Second,
		LatencyMax:    30 * time.Second,
		ErrorRate:     0,
		ErrorStatus:   500,
		ReplyText:     "Hello from the mock upstream service.",
		UsageMode:     "echo",

		ImageSyncDelayMin: 1 * time.Second,
		ImageSyncDelayMax: 30 * time.Second,
		VideoSyncDelayMin: 1 * time.Second,
		VideoSyncDelayMax: 30 * time.Second,
		SyncFailRate:      0,

		ImageDurationMin: 1 * time.Second,
		ImageDurationMax: 30 * time.Second,
		VideoDurationMin: 1 * time.Second,
		VideoDurationMax: 30 * time.Second,
		VideoConcurrency: 2,
		TaskFailRate:     0,

		RequireKey: false,
		APIKey:     "",
	}
}

// LoadConfig resolves configuration from defaults, an optional JSON file and
// environment variables (in that precedence order). The file path comes from
// the explicit `path` argument if non-empty, otherwise from MOCK_CONFIG.
// A malformed or unreadable file is a fatal misconfiguration.
func LoadConfig(path string) (Config, error) {
	cfg := defaults()

	if path == "" {
		path = os.Getenv("MOCK_CONFIG")
	}
	if path != "" {
		if err := applyFile(&cfg, path); err != nil {
			return cfg, err
		}
	}

	applyEnv(&cfg)
	return cfg, nil
}

// MustLoadConfig is a convenience wrapper that aborts on config error.
func MustLoadConfig(path string) Config {
	cfg, err := LoadConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mockupstream: config error: %v\n", err)
		os.Exit(2)
	}
	return cfg
}
