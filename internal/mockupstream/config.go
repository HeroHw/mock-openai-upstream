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
	TTFT          time.Duration // MOCK_TTFT_MS / ttft_ms：SSE 首帧前等待，模拟上游思考时间
	TokenInterval time.Duration // MOCK_TOKEN_INTERVAL_MS / token_interval_ms：流式每个 chunk 间隔，控制吐字速率
	Latency       time.Duration // MOCK_LATENCY_MS / latency_ms：非流式整体延迟
	ErrorRate     float64       // MOCK_ERROR_RATE / error_rate：错误注入率 0~1，按 model+序号哈希确定性命中
	ErrorStatus   int           // MOCK_ERROR_STATUS / error_status：注入错误时返回的 HTTP 状态码
	ReplyText     string        // MOCK_REPLY_TEXT / reply_text：chat 回包内容，可固定便于断言
	UsageMode     string        // MOCK_USAGE_MODE / usage_mode：token 用量模式，"echo"=按输入估算 | "fixed"=固定值

	// 同步生图/生视频（OpenAI 协议，§7）
	ImageSyncDelay time.Duration // MOCK_IMAGE_SYNC_DELAY_S / image_sync_delay_s：同步生图响应前阻塞秒数
	VideoSyncDelay time.Duration // MOCK_VIDEO_SYNC_DELAY_S / video_sync_delay_s：同步生视频响应前阻塞秒数
	SyncJitter     time.Duration // MOCK_SYNC_JITTER_S / sync_jitter_s：同步延时 ±抖动，按 prompt 哈希确定性计算
	SyncFailRate   float64       // MOCK_SYNC_FAIL_RATE / sync_fail_rate：同步失败注入率，按 prompt 哈希确定性命中

	// 异步生图/生视频（DashScope，§8）
	ImageDuration    time.Duration // MOCK_IMAGE_DURATION_S / image_duration_s：异步生图处理时长
	VideoDuration    time.Duration // MOCK_VIDEO_DURATION_S / video_duration_s：异步生视频处理时长
	VideoConcurrency int           // MOCK_VIDEO_CONCURRENCY / video_concurrency：视频并发槽位，超出的排队为 PENDING
	TaskJitter       time.Duration // MOCK_TASK_JITTER_S / task_jitter_s：任务时长 ±抖动，按 task_id 哈希确定性计算
	TaskFailRate     float64       // MOCK_TASK_FAIL_RATE / task_fail_rate：异步任务失败率，按 task_id 哈希命中转 FAILED

	// 可选增强（§10）
	RequireKey bool   // MOCK_REQUIRE_KEY / require_key：要求非空凭据但不校验具体值
	APIKey     string // MOCK_API_KEY / api_key：设置后强制校验，凭据须等于此固定值，否则 401
}

// defaults returns the built-in configuration used when nothing is overridden.
func defaults() Config {
	return Config{
		TTFT:          0,
		TokenInterval: 10 * time.Millisecond,
		Latency:       0,
		ErrorRate:     0,
		ErrorStatus:   500,
		ReplyText:     "Hello from the mock upstream service.",
		UsageMode:     "echo",

		ImageSyncDelay: 60 * time.Second,
		VideoSyncDelay: 60 * time.Second,
		SyncJitter:     5 * time.Second,
		SyncFailRate:   0,

		ImageDuration:    60 * time.Second,
		VideoDuration:    60 * time.Second,
		VideoConcurrency: 2,
		TaskJitter:       5 * time.Second,
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
