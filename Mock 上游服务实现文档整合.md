# Mock 上游 AI Provider 服务 — 实现文档（整合版）

> 目标：构建一个**全新的、独立的空项目**（独立 Go module / 独立仓库，与 apiqik-kratos 解耦），作为 mock 上游服务，模拟 OpenAI / Anthropic / Gemini / 阿里 DashScope 四类上游 provider 的 API。把 apiqik-kratos 网关里某渠道的 `BaseURL` 指向它，网关无需改一行代码即可在不调用真实上游、不花钱的前提下完成中转、路由、计费、SSE 流式、生图/生视频（同步与异步两种协议）的端到端验证。
>
> **本服务不在 apiqik-kratos 仓库内实现**，而是一个自包含、可单独 `go build`、可单独分发的独立项目。文中对 apiqik-kratos 代码路径（如 `internal/adapter/**`、`test/mock/*`）的引用，仅作为"要 mock 哪些端点、复刻哪些行为"的**外部参考**，不代表本项目依赖或 import 它们——所有逻辑（SSE 分片、错误模板等）都在本项目内自包含实现。
>
> 本文整合了三份文档：主实现文档、生图/生视频异步任务补充、生图/生视频同步模式补充。chat / embeddings / audio / models 见 §2~§6；生图/生视频两种模式见 §3、§7、§8。

## 1. 背景与定位

### 1.1 为什么需要它

apiqik-kratos 是 AI API 中转网关，链路是：

```
客户端 ──(OpenAI/Anthropic/Gemini 协议)──▶ 网关 ──(adapter)──▶ 上游 Provider
                                          │
                                   路由 / 计费 / 用量 / 健康检查
```

上游 provider 是链路里唯一**不可控、要花钱、有网络抖动**的一环。开发和测试时希望：

- 不花钱、不依赖外网就能把 chat / embeddings / images / audio / video 跑通；
- 能**精确控制**延迟、TTFT、token 速率、错误码、任务时长、队列并发，用于压测和故障演练；
- 响应稳定可预测，CI 里可重复。

### 1.2 定位说明

本服务是一个**独立的、长驻的 mock 上游进程**：一个 `cmd/mockupstream` 二进制，监听端口对外提供服务，可被压测工具、前端、手工联调直接访问。其 SSE 分片、错误模板等逻辑在本项目内**自行实现**，仅依赖 Go 标准库。

作为背景：apiqik-kratos 仓库内另有 `test/mock/{http_mock,sse_mock,error_mock}.go`，那是该网关的**进程内单测工具**（基于 `httptest`，只能在其 Go 单测里 `import`），与本服务分属不同项目、用途不同——本项目实现时可参考其分片/错误模板思路，但不依赖、不 import 它。

### 1.3 设计取舍（为什么这么选）

| 决策 | 选择 | 理由 |
|------|------|------|
| Mock 哪一端 | **上游 Provider** | 上游是链路里唯一外部依赖；mock 它能完整覆盖 adapter→路由→计费全链路。mock 网关对外接口只服务前端联调，价值面窄。 |
| 技术栈 | **Go 标准库 `net/http`** | 依赖最少、`go build` 单文件即可分发、启动 < 100ms；不引入 Kratos/Wire/DB，避免 mock 服务本身变重。 |
| 协议覆盖 | OpenAI 兼容优先 | 21 个上游里多数走 OpenAI-compatible 基类，一套 handler 覆盖面最大；Anthropic / Gemini 各补原生端点。 |
| 配置方式 | 环境变量 + 可选 YAML | 环境变量满足 CI/容器；YAML 用于定制场景化响应（错误注入、固定回包）。 |

## 2. 需要 mock 的上游端点

下表是参照 apiqik-kratos 网关实际调用路径（`internal/adapter/**` 里 `adapter.UpstreamEndpointURL` 的拼接规则）整理出来的，本项目按这些路径注册 handler。整理仅作外部参考，本项目不依赖该网关代码。

### 2.1 OpenAI 兼容（覆盖 openai/deepseek/moonshot/zhipu/together/stepfun 等大多数渠道）

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/v1/chat/completions` | Chat，支持 `stream=true` SSE |
| POST | `/v1/embeddings` | 文本向量 |
| POST | `/v1/images/generations` | 文生图（**同步**，见 §7） |
| POST | `/v1/images/edits` | 图像编辑，multipart（同步） |
| POST | `/v1/images/variations` | 图像变体，multipart（同步） |
| POST | `/v1/videos/generations` | 文生视频，JSON 或 multipart（同步，见 §7） |
| POST | `/v1/videos/edits` | 视频编辑，multipart（同步） |
| POST | `/v1/audio/speech` | TTS，返回音频字节 |
| POST | `/v1/audio/transcriptions` | STT，multipart 上传 |
| GET  | `/v1/models` | 模型列表 |

> 注意：部分渠道 `BaseURL` 不带 `/v1`（如 deepseek 代码里 `%s/v1/chat/completions`），mock 服务应**同时**注册 `/chat/completions` 和 `/v1/chat/completions`，对 path 做兼容前缀匹配。生图/生视频同理，带不带 `/v1` 都要兼容。

### 2.2 Anthropic 原生

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/v1/messages` | Messages，`stream=true` 走 Anthropic SSE 事件帧 |

### 2.3 Gemini 原生

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/v1beta/models/{model}:generateContent` | 非流式 |
| POST | `/v1beta/models/{model}:streamGenerateContent` | 流式 |
| POST | `/v1beta/models/{model}:countTokens` | token 计数 |

> Gemini 用 `?key=` query 传 key、`:action` 后缀区分动作，需要按 `:` 拆 path。

### 2.4 阿里 DashScope 生图/生视频（**异步**，见 §8）

| 阶段 | 方法 | 路径 | 说明 |
|------|------|------|------|
| 提交生图 | POST | `/api/v1/services/aigc/text2image/image-synthesis` | 带 `X-DashScope-Async: enable`，返回 `output.task_id` + `task_status:PENDING` |
| 提交生视频 | POST | `/api/v1/services/aigc/video-generation/video-synthesis` | 同上 |
| 轮询任务 | GET | `/api/v1/tasks/{task_id}` | 返回当前 `output.task_status` |

> 提交和查询是**两个不同路径**：查询固定走 `/api/v1/tasks/{id}`，只复用 BaseURL 的 scheme/host，不复用 service path。

## 3. 架构设计

```
mockupstream/                 （独立 Go module / 独立仓库，见 §9）
  go.mod                       module 声明，仅标准库依赖
  cmd/mockupstream/
    main.go              入口：解析 flag/env，组装 server，监听

  internal/mockupstream/
    server.go            路由注册（OpenAI/Anthropic/Gemini 三组 + DashScope 异步 handler）
  config.go            配置加载（env + 可选 YAML）
  behavior.go          行为控制：延迟、TTFT、token 速率、错误注入、同步延时
  openai.go            OpenAI 兼容 handler（chat/embeddings/audio/models）
  openai_media.go      OpenAI 同步生图/生视频 handler（sleep 延时后返回，见 §7）
  anthropic.go         Anthropic messages handler
  gemini.go            Gemini generate handler
  dashscope.go         DashScope 异步生图/生视频提交 + 轮询 handler（见 §8）
  taskqueue.go         异步任务队列：内存表 + worker + 按时间算状态的状态机
  sse.go               SSE 分片写入（自实现，可参考 test/mock 思路）
  payload.go           响应体模板 + 确定性内容生成
  assets.go            /__assets/ 静态端点：内置占位图片/视频（同步、异步共用）
  capture.go           请求录制（可选，用于断言）
```

分层原则：`main → server → 各协议 handler → behavior/sse/payload/taskqueue`。无 DB、无外部依赖，全部内存。

### 3.1 核心数据流（chat 类）

```
请求到达 handler
  → 解析协议特定 body（取 model / messages / stream 标志）
  → behavior.Apply()：按配置 sleep(TTFT)、判断是否注入错误
  → 非流式：payload 生成确定性回包 → 一次性写出
  → 流式：sse 按 token 速率分片 → flush 循环 → 结束帧
  → capture 记录请求（可选）
```

### 3.2 生图/生视频：同步 vs 异步先分清

apiqik 里生图、生视频**同时存在两种上游协议**，mock 两种都要支持，按渠道配置的 BaseURL 各走各的 handler：

| 协议 | 模式 | 行为 | 代码 | 本文 |
|------|------|------|------|------|
| OpenAI（`/images/generations`、`/videos/generations`） | **同步** | 一个 POST 挂住连接，等 ~60s 后在**同一响应**里返回结果（handler 收到请求后 `sleep` 再写响应） | `internal/adapter/openai/{images,video}_adapter.go` | §7 |
| 阿里 DashScope | **异步** | 提交立刻返回 task_id（不阻塞），再轮询 `/tasks/{id}` 直到 SUCCEEDED；耗时进任务状态机 | `internal/adapter/alibaba/{images,video}_adapter.go` | §8 |

> 同步是"延时 60s 返回"的字面意思；异步是"提交不阻塞、把 60s 耗时放进状态机里，让轮询接口 60s 后才翻成 SUCCEEDED"。两者实现完全不同。

## 4. 行为控制（压测/故障演练的关键）

通过环境变量或 YAML 配置，控制 mock 的"拟真度"。下表覆盖 chat 类通用行为；生图/生视频同步延时见 §7.1，异步任务时长/队列见 §8.3。

| 配置项 | env | 默认 | 作用 |
|--------|-----|------|------|
| 监听地址 | `MOCK_ADDR` | `:18080` | 服务端口 |
| 首字延迟 | `MOCK_TTFT_MS` | `0` | SSE 首帧前等待，模拟上游思考时间 |
| token 间隔 | `MOCK_TOKEN_INTERVAL_MS` | `10` | 流式每个 chunk 间隔，控制吐字速率 |
| 固定延迟 | `MOCK_LATENCY_MS` | `0` | 非流式整体延迟 |
| 错误注入率 | `MOCK_ERROR_RATE` | `0` | 0~1，按比例返回错误（用 model 名做确定性 hash，CI 可复现） |
| 错误码 | `MOCK_ERROR_STATUS` | `500` | 注入时返回的 HTTP 状态码 |
| 响应文本 | `MOCK_REPLY_TEXT` | 内置 | chat 回包内容，可固定便于断言 |
| token 用量 | `MOCK_USAGE_MODE` | `echo` | `echo`=按输入估算；`fixed`=固定值，便于校验计费 |
| 配置文件 | `MOCK_CONFIG` | 空 | 指向 YAML，做按 model/path 的精细化场景 |

### 4.1 错误注入的可复现性

压测/CI 要求"同样输入得到同样错误"。错误注入**不用 `rand`**，而是对 `model + 请求序号` 做哈希取模，保证：

- `MOCK_ERROR_RATE=0.1` → 每 10 个请求稳定第 10 个失败；
- 同一 model 行为确定，便于断言重试/故障转移逻辑。

> 注意：网关侧渠道健康状态机会对连续失败做熔断，演练故障转移时配合 `MOCK_ERROR_STATUS=503` 更贴近真实。同理，生图/生视频的同步/异步失败注入（§7、§8）也都用 hash 确定性命中，保证可复现。

## 5. 响应体模板（chat / embeddings 类）

### 5.1 OpenAI chat（非流式）

```json
{
  "id": "chatcmpl-mock-{seq}",
  "object": "chat.completion",
  "created": 0,
  "model": "{回显请求 model}",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "{MOCK_REPLY_TEXT}"},
    "finish_reason": "stop"
  }],
  "usage": {"prompt_tokens": N, "completion_tokens": M, "total_tokens": N+M}
}
```

`usage` 按 `MOCK_USAGE_MODE` 计算——这是**计费链路验证的关键**：`echo` 模式按输入字符估算 token，`fixed` 模式返回固定值，方便断言钱包 Hold→Settle 金额。

### 5.2 OpenAI chat（流式 SSE）

SSE 分片逻辑在本项目内自实现（可参考 `test/mock/sse_mock.go` 思路），按 `MOCK_TOKEN_INTERVAL_MS` 逐块 flush：

```
data: {"id":"chatcmpl-mock","object":"chat.completion.chunk","choices":[{"delta":{"role":"assistant"}}]}

data: {"...":"delta":{"content":"Hello"}...}
...
data: {"...":"choices":[{"delta":{},"finish_reason":"stop"}]...}

data: [DONE]
```

流式末块需带 `usage`（OpenAI 在 `stream_options.include_usage=true` 时附带），网关计费依赖它。

### 5.3 Anthropic messages（流式）

Anthropic 用具名事件帧，与 OpenAI 不同，需单独实现：

```
event: message_start
data: {...}

event: content_block_delta
data: {"delta":{"type":"text_delta","text":"Hi"}}
...
event: message_stop
data: {...}
```

### 5.4 Gemini generateContent

返回 `candidates[].content.parts[].text` + `usageMetadata`，流式同理逐 candidate chunk。

## 6. 接入方式

### 6.1 启动

```bash
# 默认配置启动
go run ./cmd/mockupstream
# → mock upstream listening on :18080

# 压测场景：模拟 200ms 首字 + 20ms/token
MOCK_TTFT_MS=200 MOCK_TOKEN_INTERVAL_MS=20 go run ./cmd/mockupstream

# 故障演练：30% 请求返回 503
MOCK_ERROR_RATE=0.3 MOCK_ERROR_STATUS=503 go run ./cmd/mockupstream
```

（生图/生视频专项启动示例见 §7.4、§8.5。）

### 6.2 让网关用上它

把目标渠道的 `BaseURL` 指向 mock 服务即可（数据库 `channels` 表或测试夹具）：

```
BaseURL: http://localhost:18080
APIKey:  任意非空字符串（mock 不校验，或按 §10 校验）
```

`adapter.UpstreamEndpointURL` 会基于该 BaseURL 拼出 `/v1/chat/completions` 等路径，命中 mock handler。**网关代码零改动。** 同一渠道下，OpenAI 类走同步生图/生视频、阿里类走异步，两条 handler 互不干扰。

### 6.3 docker-compose 集成

在 `docker-compose.test.yml` 增加一个 service，与 app 同网络，渠道 BaseURL 写 `http://mockupstream:18080`，CI 一键拉起。

## 7. 生图 / 生视频 — 同步模式（OpenAI 协议）

OpenAI 的 `/images/generations`、`/videos/generations` 在上游是**同步**的：一个 POST 挂住连接，等约 60s 后在同一响应里返回结果。handler 流程极简：

```
收到 POST /v1/images/generations
  → 解析 body 取 model / n / size / response_format
  → behavior：sleep(MOCK_IMAGE_SYNC_DELAY_S ± jitter)   // 默认 60s
  → 按 n 生成 n 条结果 URL（或 b64_json）
  → 一次性写出 {created, data:[...]}
```

关键点：
- **延时在响应前 sleep**，连接一直挂着——这正是要验证网关 `imagesDefaultTimeoutMS` / `videoDefaultTimeoutMS`（10 分钟）超时配置是否够、HTTP server ResponseHeaderTimeout 是否被打满的场景（见归档文档「30秒超时问题分析」「ResponseHeaderTimeout问题根源」）。
- 延时要**可配且可超过常见网关超时**，用来主动触发超时分支演练。
- `response_format=url` 返回 mock 自身 `/__assets/` 的 URL；`b64_json` 返回内置占位图的 base64。

### 7.1 同步模式配置项

| 配置项 | env | 默认 | 作用 |
|--------|-----|------|------|
| 生图同步延时 | `MOCK_IMAGE_SYNC_DELAY_S` | `60` | 响应前阻塞秒数 |
| 生视频同步延时 | `MOCK_VIDEO_SYNC_DELAY_S` | `60` | 响应前阻塞秒数 |
| 延时抖动 | `MOCK_SYNC_JITTER_S` | `5` | ±抖动，按请求体 hash 确定性计算，CI 可复现 |
| 超时演练 | `MOCK_IMAGE_SYNC_DELAY_S=700` | — | 设到大于网关超时（如 >600s），主动触发超时 |
| 失败注入 | `MOCK_SYNC_FAIL_RATE` | `0` | 按 prompt hash 命中则返回 4xx/5xx 错误体 |

### 7.2 同步响应体模板

**生图 SUCCEEDED（url 格式）**，`data` 数组长度 = 请求里的 `n`（默认 1）：

```json
{
  "created": 0,
  "data": [
    {"url": "http://localhost:18080/__assets/mock-image.png", "revised_prompt": "{回显 prompt}"}
  ]
}
```

**生图（b64_json 格式）**：

```json
{
  "created": 0,
  "data": [
    {"b64_json": "iVBORw0KGgoAAAANS...（内置占位图 base64）"}
  ]
}
```

**生视频**：

```json
{
  "created": 0,
  "data": [
    {"url": "http://localhost:18080/__assets/mock-video.mp4"}
  ]
}
```

> 结果 URL 指向 mock 自身 `/__assets/` 静态端点（与异步共用，见 §8.4），回内置小图/小视频，确保网关后续下载、OSS/RustFS 转存链路也能跑通。

**错误（注入失败时）**，HTTP 状态码用 5xx 或 4xx，触发网关错误处理与计费回滚：

```json
{
  "error": {
    "type": "server_error",
    "message": "mock injected failure",
    "code": "internal_error"
  }
}
```

### 7.3 同步模式验收标准

- `POST /v1/images/generations` 阻塞约 60s（±抖动）后返回，`data` 长度等于请求 `n`；
- `response_format=url` 时 URL 可被网关下载，转存链路打通；`b64_json` 时返回合法 base64；
- 延时设到 >网关超时时，网关稳定走超时分支并正确回滚计费（Hold→Release）；
- `MOCK_SYNC_FAIL_RATE>0` 时按 prompt 稳定复现错误，触发错误处理；
- 同一渠道下，OpenAI 走同步、阿里走异步，两条 handler 互不干扰。

### 7.4 同步模式启动示例

```bash
# 生图/生视频同步约 60s 返回
MOCK_IMAGE_SYNC_DELAY_S=60 MOCK_VIDEO_SYNC_DELAY_S=60 go run ./cmd/mockupstream

# 超时演练：延时 700s，超过网关 600s 超时，验证超时分支
MOCK_IMAGE_SYNC_DELAY_S=700 go run ./cmd/mockupstream
```

把 OpenAI 类渠道 `BaseURL` 指向 `http://localhost:18080`，网关发 `images.generate` / `videos.generate` 后，约 60s 在同一请求里拿到结果。

## 8. 生图 / 生视频 — 异步任务模式（阿里 DashScope）

阿里万相的生图、生视频在上游就是异步的（见 `internal/adapter/alibaba/{images,video}_adapter.go`）：

```
网关 ──POST 提交任务──▶ 上游    立刻返回 task_id + PENDING（毫秒级，不阻塞）
网关 ──GET 轮询 /tasks/{id}──▶ 上游    PENDING → RUNNING → SUCCEEDED/FAILED
        （生图 2s/次，生视频 3s/次，直到完成或超时）
```

mock 要做的不是"sleep 60s 再返回提交请求"，而是：**提交瞬间返回 task_id，把 60s 的耗时放进任务状态机里**，让轮询接口在 60s 后才把状态翻成 SUCCEEDED。这样才贴合真实链路，也才能验证网关的轮询、超时、并发槽位逻辑。

要点（mock 必须复刻的真实行为）：
- 提交和查询是**两个不同路径**：查询固定走 `/api/v1/tasks/{id}`，只复用 BaseURL 的 scheme/host，不复用 service path。
- 提交**立刻返回**，`task_status=PENDING`，不阻塞连接。
- 网关侧轮询参数（mock 的完成时间必须落在超时内）：生图 2s/次、超时 5min；生视频 3s/次、超时 10min。
- 失败原因放在 `output.code` / `output.message`（任务级），与顶层 `code`（请求级）区分。

### 8.1 任务结构

mock 内部维护一个**内存任务表 + 后台队列**（`taskqueue.go`）。

```go
type Task struct {
    ID         string
    Kind       string    // "image" | "video"
    Model      string
    SubmitAt   time.Time
    StartAt    time.Time // 进入 RUNNING 的时间
    FinishAt   time.Time // 预定完成时间 = StartAt + 处理时长
    Status     string    // PENDING / RUNNING / SUCCEEDED / FAILED
    ResultURLs []string  // 完成后填充
    ErrCode    string    // 失败时填充
    ErrMessage string
}
```

### 8.2 状态推进（按时间计算，而非定时器）

查询时**按当前时间算状态**，不依赖后台定时器精度，简单且无竞态：

```
查询 task：
  now := time.Now()
  if now < StartAt        → PENDING   (排队中)
  else if now < FinishAt  → RUNNING   (处理中)
  else                    → SUCCEEDED (或预设的 FAILED)
```

- `StartAt = SubmitAt + 排队时长`：模拟队列等待。空闲时排队时长≈0；并发超限时往后排。
- `FinishAt = StartAt + 处理时长`：处理时长就是你要的"任务执行 60s 左右"。

**队列容量与排队（视频的关键）**：视频要"模拟个队列"，即**有限并发**，超出的排队。提交时分配 `StartAt`：

- 当前 RUNNING 数 < 并发上限（`MOCK_VIDEO_CONCURRENCY`）→ `StartAt = now`（立即开始）；
- 已满 → `StartAt = 最早一个将完成任务的 FinishAt`（排在它后面）。

这样第 3 个视频任务会先 PENDING 一段时间，等前面腾出槽位才转 RUNNING，真实复刻队列行为。生图也可共用此机制（默认并发更高或不限）。

### 8.3 异步时长配置

| 配置项 | env | 默认 | 作用 |
|--------|-----|------|------|
| 生图处理时长 | `MOCK_IMAGE_DURATION_S` | `60` | 提交后约 60s 轮询到 SUCCEEDED |
| 生视频处理时长 | `MOCK_VIDEO_DURATION_S` | `60` | 同上 |
| 视频并发槽位 | `MOCK_VIDEO_CONCURRENCY` | `2` | 超出则排队，PENDING 等待 |
| 时长抖动 | `MOCK_TASK_JITTER_S` | `5` | 在基准时长上 ±抖动，更拟真（确定性，按 task_id hash） |
| 任务失败率 | `MOCK_TASK_FAIL_RATE` | `0` | 按 task_id 确定性命中，转 FAILED 演练失败分支 |

> "60s 左右" = `MOCK_IMAGE_DURATION_S` ± `MOCK_TASK_JITTER_S`，抖动用 task_id 哈希保证可复现。

### 8.4 异步响应体模板

**提交生图 / 生视频（立即返回）**：

```json
{
  "request_id": "mock-req-{seq}",
  "output": {
    "task_id": "mock-task-{uuid}",
    "task_status": "PENDING"
  }
}
```

**轮询 — PENDING / RUNNING**：

```json
{
  "request_id": "mock-req-{seq}",
  "output": {
    "task_id": "mock-task-xxx",
    "task_status": "RUNNING"
  }
}
```

**轮询 — 生图 SUCCEEDED**：

```json
{
  "request_id": "mock-req-{seq}",
  "output": {
    "task_id": "mock-task-xxx",
    "task_status": "SUCCEEDED",
    "results": [
      {"url": "http://localhost:18080/__assets/mock-image.png"}
    ]
  },
  "usage": {"image_count": 1}
}
```

**轮询 — 生视频 SUCCEEDED**：

```json
{
  "request_id": "mock-req-{seq}",
  "output": {
    "task_id": "mock-task-xxx",
    "task_status": "SUCCEEDED",
    "video_url": "http://localhost:18080/__assets/mock-video.mp4"
  },
  "usage": {"video_count": 1}
}
```

> 结果 URL 指向 mock 自身的 `/__assets/` 静态端点（与同步模式共用），返回内置小图/小视频，确保网关下载/转存（OSS/RustFS）链路也能跑通。

**轮询 — FAILED**：

```json
{
  "request_id": "mock-req-{seq}",
  "output": {
    "task_id": "mock-task-xxx",
    "task_status": "FAILED",
    "code": "InternalError.Timeout",
    "message": "mock injected failure"
  }
}
```

### 8.5 异步模式启动示例

```bash
# 生图/生视频都约 60s 完成，视频并发 2
MOCK_IMAGE_DURATION_S=60 MOCK_VIDEO_DURATION_S=60 MOCK_VIDEO_CONCURRENCY=2 \
  go run ./cmd/mockupstream

# 演练视频队列：并发只给 1，连发 3 个任务观察排队
MOCK_VIDEO_CONCURRENCY=1 go run ./cmd/mockupstream
```

把阿里渠道 `BaseURL` 指向 `http://localhost:18080`，网关提交生图/生视频后会自动轮询，约 60s 后拿到结果 URL。

### 8.6 异步模式验收标准

- 提交接口毫秒级返回 `task_id` + `PENDING`，不阻塞；
- 轮询接口在约 60s（±抖动）后才返回 `SUCCEEDED`，期间是 `PENDING`/`RUNNING`；
- `MOCK_VIDEO_CONCURRENCY=1` 时连发 3 个视频任务，第 2、3 个明显先排队（PENDING 持续更久）；
- 完成后结果 URL 可被网关下载，OSS/RustFS 转存链路打通；
- `MOCK_TASK_FAIL_RATE>0` 时能稳定复现 FAILED，触发网关任务失败分支与计费回滚（Hold→Release）。

## 9. 代码落地位置：独立项目

本服务作为**全新的独立项目**实现，不放进 apiqik-kratos 仓库：

- 独立 `go.mod`（如 `module github.com/<org>/mockupstream`），**仅依赖 Go 标准库**，完全解耦；
- 自包含 SSE 分片、错误模板等逻辑（参考但不 import apiqik-kratos 的 `test/mock`）；
- 可单独 `go build ./cmd/mockupstream` 产出单文件二进制，可单独分发给前端/测试团队，可单独打 Docker 镜像；
- apiqik-kratos 网关侧**零改动**，只需把渠道 `BaseURL` 指向本服务即可联调。

> 取舍：与仓库内方案相比，独立项目需要自行实现一份 SSE/错误模板逻辑，但换来完全解耦、可独立分发、不污染主仓库依赖树。本服务功能边界清晰、依赖极少，自实现成本低，独立项目是合适选择。

## 10. 可选增强（按需）

- **请求录制回放**：`capture.go` 把收到的请求落盘，支持 `--record` / `--replay`，做契约测试。
- **API Key 校验开关**：`MOCK_REQUIRE_KEY=1` 时校验 `Authorization`，验证网关凭据透传是否正确。
- **管理端点**：`POST /__mock/config` 运行时热改行为，免重启切换场景。
- **健康端点**：`GET /__mock/healthz`，供 compose `healthcheck` 探活。

> 安全提示：mock 服务**不做认证**是刻意设计（降低联调成本），因此**只能监听内网/本地**，禁止暴露到公网。文档和启动日志需明确这一点。

## 11. 实施步骤建议

按依赖顺序，分三段推进。

**第一段：chat 类最小闭环**
1. 新建独立空项目：`go mod init`，建 `cmd/mockupstream/main.go` + `internal/mockupstream/server.go`，先跑通 OpenAI 非流式 chat。
2. 加 SSE 流式 chat，自实现分片逻辑（可参考 apiqik-kratos `test/mock/sse_mock.go`）。
3. 补 embeddings / audio / models。
4. 加 behavior 行为控制（延迟、错误注入、token 速率）。
5. 补 Anthropic messages、Gemini generateContent 两个原生协议。
6. 集成测试：起 mock → 配置渠道 BaseURL → 走网关发 chat → 断言计费 usage。
7. 接入 `docker-compose.test.yml`，CI 拉起。

**第二段：生图/生视频异步任务（DashScope）**
8. 加 `taskqueue.go` 内存任务表 + 按时间算状态的状态机。
9. 加 `dashscope.go`：提交 handler（入队返回 PENDING）+ 轮询 handler（按时间返回状态）。
10. 加 `/__assets/` 静态资源端点，内置占位图片/视频。
11. 接入并发槽位与排队逻辑（视频队列）。
12. 集成测试：起 mock → 配阿里渠道 → 走网关提交生视频 → 轮询直到 SUCCEEDED → 断言耗时≈60s、计费正确。

**第三段：生图/生视频同步模式（OpenAI）**
13. 加 `openai_media.go`：同步生图/生视频 handler，sleep 延时后返回。
14. 复用 `/__assets/` 静态端点（与异步共用）。
15. behavior 接入同步延时、抖动、失败注入。
16. 集成测试：起 mock → 配 OpenAI 渠道 → 发同步生图 → 断言耗时≈60s、`data` 正确、计费正确；再单独跑一个超时演练用例。

## 12. 总验收标准

- 网关把某渠道 BaseURL 指向 mock 后，OpenAI/Anthropic/Gemini 三协议的 chat 非流式 + 流式均能正常返回，计费 usage 被正确结算；
- `MOCK_ERROR_RATE` 能稳定复现错误，触发网关健康状态机与故障转移；
- 压测下 `MOCK_TTFT_MS` / `MOCK_TOKEN_INTERVAL_MS` 生效，TTFT 和吐字速率可观测；
- **同步生图/生视频**（§7）：阻塞约 60s 返回、`data` 长度等于 `n`、URL 可下载转存；延时超过网关超时时稳定走超时分支并回滚计费；
- **异步生图/生视频**（§8）：提交毫秒级返回 PENDING、轮询约 60s 后 SUCCEEDED、视频并发限制下排队可观测、失败注入触发任务失败分支与计费回滚；
- 服务启动 < 1s，无 DB/Redis/外网依赖。
