# mockupstream

模拟 OpenAI / Anthropic / Gemini / 阿里 DashScope 四类上游 provider API 的独立 mock 服务。把网关某渠道的 `BaseURL` 指向它，即可在**不调真实上游、不花钱、不依赖外网**的前提下，验证路由、计费、SSE 流式、生图/生视频（同步与异步两种协议）的端到端链路。

仅依赖 Go 标准库，启动 < 1s，无 DB、无 Redis、无外部依赖。

> **安全提示：** 本服务默认**不做认证**（刻意设计，降低联调成本）。只能监听本地 / 内网，**禁止暴露到公网**。

## 配置优先级

三层叠加，后者覆盖前者：

```
内置默认值  <  配置文件（JSON）  <  环境变量
```

零配置即可跑；配置文件用于固化一个场景；环境变量可临时覆盖任意单项（适合 CI/容器）。配置文件路径由 `-config` 命令行参数指定，未指定则回退到 `MOCK_CONFIG` 环境变量。文件不存在或键名拼错会直接报错退出（而非静默忽略）。

配置文件是 JSON（仅依赖标准库，不引入 YAML 解析器）。字段名见 `config.example.json`，时间字段沿用与环境变量一致的单位（`*_ms` 毫秒、`*_s` 秒）：

```bash
# 用配置文件启动
go run ./cmd/mockupstream -config ./config.example.json

# 或通过环境变量指定路径
MOCK_CONFIG=./config.example.json go run ./cmd/mockupstream

# 配置文件打底，环境变量临时覆盖某一项（如错误注入率）
go run ./cmd/mockupstream -config ./config.example.json
MOCK_ERROR_RATE=0.3 go run ./cmd/mockupstream -config ./config.example.json  # 覆盖文件里的 error_rate
```

> 监听端口固定为 `:18080`，不可配置。本地跑就是 `:18080`；容器里要换对外端口，改 docker-compose 的端口映射（见下文）。

`config.example.json` 列出了全部可配字段，可直接复制改用。每个字段的中文说明见 `config.example.jsonc`（带注释的参考文件，仅供阅读，不能直接加载——JSON 标准不支持注释）。

## 启动

> 注意：本机 Go 装在 `/opt/homebrew/bin`，若当前 shell 的 PATH 里没有它，先 `export PATH=/opt/homebrew/bin:$PATH`。

```bash
# 默认配置启动，监听 :18080
go run ./cmd/mockupstream
# → [mockupstream] listening on :18080 (no auth — bind to internal network only)

# 编译成单文件二进制（便于分发、反复启动）
go build -o mockupstream ./cmd/mockupstream && ./mockupstream

# 压测场景：200ms 首字 + 20ms/token
MOCK_TTFT_MS=200 MOCK_TOKEN_INTERVAL_MS=20 go run ./cmd/mockupstream

# 故障演练：30% 请求返回 503
MOCK_ERROR_RATE=0.3 MOCK_ERROR_STATUS=503 go run ./cmd/mockupstream
```

让网关用上它（数据库 `channels` 表或测试夹具）：

```
BaseURL: http://localhost:18080
APIKey:  任意非空字符串（默认不校验，除非 MOCK_REQUIRE_KEY=1）
```

`adapter.UpstreamEndpointURL` 会基于该 BaseURL 拼出 `/v1/chat/completions` 等路径，命中 mock handler，**网关代码零改动**。

## 端点

| 协议 | 端点 |
|------|------|
| OpenAI 兼容 | `/v1/chat/completions`（SSE）、`/v1/responses`（Responses API，具名事件帧 SSE）、`/v1/embeddings`、`/v1/images/generations`·`/edits`·`/variations`、`/v1/videos/generations`·`/edits`、`/v1/audio/speech`·`/transcriptions`、`/v1/models` |
| Anthropic | `/v1/messages`（具名事件帧 SSE，支持 extended thinking） |
| Gemini | `/v1beta/models/{model}:generateContent`·`:streamGenerateContent`·`:countTokens`（TTS 模型返回 `inlineData` 音频） |
| DashScope（异步） | `POST .../{text2image,image2image}/image-synthesis`、`POST .../{video-generation,image2video}/video-synthesis`（覆盖 wan2.x / happyhorse 全系）、`GET /api/v1/tasks/{id}` |
| 智谱 GLM | `/api/paas/v4/chat/completions`（SSE）、`/api/paas/v4/images/generations`（同步）、`POST /api/paas/v4/videos/generations`（异步提交，CogVideoX）、`GET /api/paas/v4/async-result/{id}`（轮询，返回 `video_result`） |
| MiniMax 海螺（异步） | `POST /v1/video_generation`（提交）、`GET /v1/query/video_generation?task_id=`（轮询）、`GET /v1/files/retrieve?file_id=`（成功后取 `download_url`） |
| 内部 | `/__assets/{mock-image.png,mock-video.mp4,mock-audio.wav}`、`/__mock/healthz`、`/__mock/requests`（请求捕获）、`/__mock/behavior`（按需错误注入） |

路径按后缀匹配，所以 `BaseURL` 带不带 `/v1` 前缀都能正确路由。

### 思考模型（reasoning_content / thinking）

命中以下任一条件时，chat 回包（含流式）在正文前额外携带思考内容：

- 模型名含 `thinking` / `reasoner`（如 `qwen-plus-thinking`、`deepseek-reasoner`）；
- 请求带 `"enable_thinking": true`（Qwen/DashScope 风格）；
- 请求带 `"thinking": {"type": "enabled"}`（豆包 doubao-seed / 智谱 glm-5.x / Anthropic 风格）。

OpenAI 兼容与智谱端点在 message/delta 里输出 `reasoning_content`；Anthropic 端点输出 `thinking` 内容块（流式为 `thinking_delta` + `signature_delta` 事件，text 块 index 顺延）；`/v1/responses` 请求带 `"reasoning": {...}` 时，output 数组多一个 `reasoning` 项（流式先发 `response.reasoning_summary_text.delta`），usage 带 `output_tokens_details.reasoning_tokens`。思考文本固定，便于断言。

`/v1/models` 返回覆盖各厂商热门模型的静态列表（gpt-5.5、claude-fable-5、deepseek-v3.1、qwen-turbo-thinking、kimi-k2.7-code、glm-5.2、doubao-seed-2-0-pro-260215、gpt-image-2、wan2.6-t2i、doubao-seedream-5-0-260128、gpt-4o-mini-tts、gemini-3.1-flash-tts-preview、wan2.7 全系、happyhorse-1.1 全系、MiniMax-Hailuo-2.3 等）。

### 媒体资产：真实可播放的数据

`/__assets/` 下的三种素材都是**真实可解码/可播放**的数据，而非仅结构合法的空壳：

| 资产 | 内容 | 来源 |
|------|------|------|
| `mock-image.png` | 512×512 渐变测试卡（带网格线） | 启动时用标准库 `image/png` 生成，确定性 |
| `mock-audio.wav` | 440Hz 正弦波 2 秒（16-bit/24kHz 单声道，约 94KB） | 启动时生成，确定性 |
| `mock-video.mp4` | Big Buck Bunny 10 秒 H.264 片段（360p，约 1MB） | `go:embed` 内嵌（CC-BY，取自 test-videos.co.uk） |

引用它们的所有链路都拿到真数据：同步/异步生图生视频的 `url`、`b64_json`（可 base64 解码回真实 PNG/MP4）、`/v1/audio/speech` 的音频字节、Gemini TTS 的 `inlineData`、MiniMax 的 `download_url`。

想换成自己的素材：设 `MOCK_ASSETS_DIR` 指向一个目录，放入同名文件即可逐个覆盖（缺的文件继续用内置），无需重新编译：

```bash
MOCK_ASSETS_DIR=./my-assets go run ./cmd/mockupstream
# my-assets/mock-video.mp4 存在 → 视频用你的；图片/音频没放 → 用内置
```

## 配置（环境变量）

下表列出环境变量；每项在配置文件里都有对应的 JSON 键（见 `config.example.json`）。

| 变量 | 默认 | 作用 |
|------|------|------|
| `MOCK_TTFT_MS` | `0` | SSE 首帧前等待，模拟上游思考时间 |
| `MOCK_TOKEN_INTERVAL_MS` | `10` | 流式每个 chunk 间隔，控制吐字速率 |
| `MOCK_LATENCY_MS` | `0` | 非流式整体延迟 |
| `MOCK_ERROR_RATE` | `0` | 0~1，确定性错误注入（按 model+序号哈希，CI 可复现） |
| `MOCK_ERROR_STATUS` | `500` | 注入时返回的 HTTP 状态码 |
| `MOCK_REPLY_TEXT` | 内置 | chat 回包内容，可固定便于断言 |
| `MOCK_USAGE_MODE` | `echo` | `echo` 按输入估算 token；`fixed` 返回固定值，便于校验计费 |
| `MOCK_CACHE_READ_TOKENS` | `0` | 缓存读取 token 数（Anthropic `cache_read_input_tokens`、OpenAI `cached_tokens`、Gemini `cachedContentTokenCount`） |
| `MOCK_CACHE_CREATION_5M_TOKENS` / `MOCK_CACHE_CREATION_1H_TOKENS` | `0` | 5 分钟 / 1 小时 TTL 缓存创建 token 数（Anthropic `cache_creation.ephemeral_5m_input_tokens` / `.ephemeral_1h_input_tokens`；总量 `cache_creation_input_tokens` = 两者之和） |
| `MOCK_CACHE_CREATION_TOKENS` | `0` | 旧字段（兼容保留）；未设置 5m/1h 拆分时充当 5m 档的值 |
| `MOCK_IMAGE_INPUT_TOKENS` / `MOCK_IMAGE_OUTPUT_TOKENS` | `0` | 图片输入/输出 token 数（Gemini `promptTokensDetails` / `candidatesTokensDetails` 的 IMAGE 模态） |
| `MOCK_AUDIO_INPUT_TOKENS` / `MOCK_AUDIO_OUTPUT_TOKENS` | `0` | 音频输入/输出 token 数（OpenAI `prompt_tokens_details.audio_tokens` / `completion_tokens_details.audio_tokens`、Gemini AUDIO 模态） |
| `MOCK_IMAGE_SYNC_DELAY_S` / `MOCK_VIDEO_SYNC_DELAY_S` | `60` | 同步生图/生视频响应前阻塞秒数 |
| `MOCK_SYNC_JITTER_S` | `5` | 同步延时 ±抖动（按 prompt 哈希，确定性） |
| `MOCK_SYNC_FAIL_RATE` | `0` | 同步失败注入（按 prompt 哈希，确定性） |
| `MOCK_IMAGE_DURATION_S` / `MOCK_VIDEO_DURATION_S` | `60` | 异步任务处理时长 |
| `MOCK_VIDEO_CONCURRENCY` | `2` | 异步视频并发槽位，超出的排队为 PENDING |
| `MOCK_TASK_JITTER_S` | `5` | 任务时长 ±抖动（按 task_id 哈希，确定性） |
| `MOCK_TASK_FAIL_RATE` | `0` | 异步失败注入（按 task_id 哈希，确定性） |
| `MOCK_REQUIRE_KEY` | `false` | 要求非空凭据（Authorization / x-api-key / ?key=），但不校验具体值 |
| `MOCK_API_KEY` | 空 | 设置后强制校验：凭据必须**等于**此固定值（常量时间比较），否则返回 401 |
| `MOCK_ASSETS_DIR` | 空 | 真实素材目录；内含 `mock-image.png`/`mock-video.mp4`/`mock-audio.wav` 时逐个覆盖内置资产 |

### 鉴权说明

默认零认证。设置 `MOCK_API_KEY` 后即开启固定值校验，请求必须带上匹配的凭据，三种方式任选其一：

```bash
MOCK_API_KEY=sk-mock-secret go run ./cmd/mockupstream
```

```bash
# Bearer（OpenAI / Anthropic 风格）
curl localhost:18080/v1/chat/completions \
  -H "Authorization: Bearer sk-mock-secret" \
  -d '{"model":"m","messages":[{"role":"user","content":"hi"}]}'

# x-api-key 头
curl ... -H "x-api-key: sk-mock-secret" ...

# Gemini 的 ?key= 查询参数
curl "localhost:18080/v1beta/models/gemini-pro:generateContent?key=sk-mock-secret" ...
```

凭据缺失或不匹配返回 `401`。内部端点 `/__mock/*`、`/__assets/*` 不受校验，compose healthcheck 照常工作。`MOCK_API_KEY` 一旦设置即隐含开启校验，无需再设 `MOCK_REQUIRE_KEY`。

所有"随机性"都是确定性的——决策来自请求输入的稳定哈希，因此同一压测负载在多次运行间复现相同的延迟和错误，CI 友好。

## 验收辅助端点（请求捕获 + 按需错误注入）

为网关渠道功能验收（参数覆盖、请求头覆盖/剔除、透传请求体、自动禁用）提供两组内部端点。与 `/__mock/healthz` 一样**不受 API-key 校验和错误注入影响**。

### 请求捕获 `/__mock/requests`

所有业务请求（`/__mock/*`、`/__assets/*` 除外）的最终形态——方法、路径、query、**完整请求头**、请求体——都会记入内存环形缓冲（最近 256 条）。用它断言网关下发到上游的请求：头覆盖/剔除是否生效、参数覆盖后的 body、透传模式下 body 是否字节级一致（比对 `body_sha256`）。

```bash
# 查看最近的请求（默认 20 条，最新在前）
curl 'localhost:18080/__mock/requests'

# 按条数和路径后缀过滤
curl 'localhost:18080/__mock/requests?limit=5&path_suffix=/chat/completions'

# 清空捕获记录（每个用例开始前清一次，断言才干净）
curl -X DELETE localhost:18080/__mock/requests
```

返回字段说明：`headers` 为完整请求头；`body` 为 UTF-8 文本原文（二进制则给 `body_base64`）；`body_sha256`/`body_bytes` 覆盖完整 payload（存储副本超过 1 MiB 截断并标记 `body_truncated`，但摘要仍是全量的）；`seq` 单调递增，便于排序断言。

典型断言（配合 jq）：

```bash
# 断言参数覆盖：上游收到的 temperature 是渠道覆盖值
curl -s 'localhost:18080/__mock/requests?limit=1&path_suffix=/chat/completions' \
  | jq -r '.requests[0].body | fromjson | .temperature'

# 断言头剔除：X-Debug-Token 不应出现
curl -s 'localhost:18080/__mock/requests?limit=1' \
  | jq '.requests[0].headers | has("X-Debug-Token")'   # → false

# 断言透传：sha256 与客户端原始 body 一致
shasum -a 256 payload.json
curl -s 'localhost:18080/__mock/requests?limit=1' | jq -r '.requests[0].body_sha256'
```

### 按需错误注入 `/__mock/behavior`

与 `MOCK_ERROR_RATE`（哈希采样、启动时配置）不同，这里是**运行时设定、确定性生效**的失败规则：接下来 N 次请求必然返回指定状态码和消息——正是渠道自动禁用（60 秒窗口内失败 N 次）演练需要的形态。全局只有一条规则，新 POST 覆盖旧规则。

```bash
# 接下来 3 次请求返回 429（触发 auto-disable 阈值），之后自动恢复
curl -X POST localhost:18080/__mock/behavior \
  -d '{"status":429,"message":"rate limit exceeded (mock)","times":3}'

# message 可埋 auto-disable 的 keywords 关键词
curl -X POST localhost:18080/__mock/behavior \
  -d '{"status":401,"message":"invalid api key provided","times":3}'

# times=0 表示不限次（直到 DELETE）；path_suffix 只对匹配路径生效
curl -X POST localhost:18080/__mock/behavior \
  -d '{"status":503,"times":0,"path_suffix":"/chat/completions"}'

# 查看当前规则（含已命中次数 hits）／清除规则
curl localhost:18080/__mock/behavior
curl -X DELETE localhost:18080/__mock/behavior
```

字段：`status` HTTP 状态码（默认 500）；`message` 错误消息，放进 OpenAI 风格 error 信封；`times` 剩余失败次数，>0 耗尽后规则自动移除、0 为不限次；`path_suffix` 路径后缀过滤，空则命中所有业务路径。错误注入发生在协议分发之前，对所有端点生效；被注入的请求同样会被捕获进 `/__mock/requests`。

## 同步 vs 异步生图/生视频

- **同步（OpenAI）**：一个 POST 挂住连接，等 `*_SYNC_DELAY_S`（默认 60s）后在**同一响应**里返回结果。把延时设到大于网关超时，即可主动触发超时分支演练。
- **异步（DashScope）**：提交瞬间返回 `task_id` + `PENDING`，耗时进入按时间计算的状态机，轮询 `/api/v1/tasks/{id}` 在 `*_DURATION_S` 后才翻成 `SUCCEEDED`。视频有并发上限，超出的任务排队为 `PENDING`。
- **异步（智谱 CogVideoX）**：同一套时间状态机，但走智谱信封——提交返回 `id` + `PROCESSING`，轮询 `/api/paas/v4/async-result/{id}`，完成后翻成 `SUCCESS` 并带 `video_result`（含 `url`、`cover_image_url`），失败为 `FAIL`。
- **异步（MiniMax 海螺）**：同一套时间状态机，走 MiniMax 信封——提交 `/v1/video_generation` 返回 `task_id`（`base_resp.status_code=0`），轮询 `/v1/query/video_generation?task_id=` 状态为 `Queueing`/`Processing`/`Success`/`Fail`，成功后拿 `file_id` 到 `/v1/files/retrieve?file_id=` 换取 `download_url`。

快速联调时把时长缩短，免等 60s：

```bash
# 同步、异步都缩到 2s
MOCK_IMAGE_SYNC_DELAY_S=2 MOCK_VIDEO_SYNC_DELAY_S=2 \
MOCK_IMAGE_DURATION_S=2 MOCK_VIDEO_DURATION_S=2 go run ./cmd/mockupstream

# 超时演练：同步延时 700s，超过网关 600s 超时
MOCK_IMAGE_SYNC_DELAY_S=700 go run ./cmd/mockupstream

# 视频队列演练：并发只给 1，连发 3 个观察排队
MOCK_VIDEO_CONCURRENCY=1 go run ./cmd/mockupstream
```

## 测试

```bash
go test ./...
```

## Docker / docker-compose 部署

镜像采用多阶段构建：用 `golang:1.26-alpine` 编译出静态二进制，最终塞进 `scratch` 空镜像（无 shell、非 root、约 9MB）。

```bash
# 直接用 docker
docker build -t mockupstream:latest .
docker run --rm -p 127.0.0.1:18080:18080 mockupstream:latest

# 或用 docker-compose（推荐）
docker compose up -d --build      # 或旧版 docker-compose up -d --build
docker compose down
```

要点：
- **监听端口固定为容器内 `:18080`**，不可配置。要换对外端口，改 `docker-compose.yml` 里 `ports` 的左侧宿主端口，例如 `"9000:18080"` 即可在 `localhost:9000` 访问；用 `docker run` 时改 `-p` 同理。
- **端口只绑定到宿主 loopback**（`127.0.0.1:18080:18080`），因为本服务默认不做认证，避免暴露到公网。
- **健康检查**复用二进制自带的 `-healthcheck` 自检模式（scratch 镜像里没有 curl），它会探测本地 `/__mock/healthz` 并以退出码 0/1 反馈，compose `healthcheck` 直接调它。
- 改行为：在 `docker-compose.yml` 的 `environment` 里取消注释相应 `MOCK_*` 变量；或挂载 JSON 配置文件并加 `command: ["-config", "/config.json"]`（环境变量仍优先于文件）。
- 网关接入：与本服务同网络时把渠道 `BaseURL` 写 `http://mockupstream:18080`；从宿主机访问用 `http://localhost:18080`。

## 验证服务存活

```bash
curl localhost:18080/__mock/healthz
# {"status":"ok"}
```

前台运行时按 `Ctrl+C` 优雅关闭。
