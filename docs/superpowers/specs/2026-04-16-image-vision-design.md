# 设计文档：图片视觉能力（LLM Native Vision）

**日期**: 2026-04-16
**状态**: 已批准

## 背景

当前图片上传流程：前端上传 → 文件写入沙箱 `/home/gem/uploads/` → 沙箱路径以文本形式告知模型。模型只能通过 `read_file` 工具读取字节或调用 ImageMagick 处理图片，未能利用 LLM 的原生视觉能力。

目标：图片上传后，由后端从沙箱下载图片字节、base64 编码，以 `image` content block 形式传入 LLM，让模型直接"看到"图片。

## 范围

- 支持三个 LLM provider：Claude、OpenAI、Gemini
- 内存缓存 base64，同一张图片在会话多轮中只下载一次
- 缓存 miss 时安全降级为文本路径描述（历史会话兼容）

## 架构

### 1. 数据模型扩展（`llm/client.go`）

在 `ContentBlock` 新增图片字段：

```go
type ContentBlock struct {
    Type string `json:"type"`
    Text string `json:"text,omitempty"`

    // tool_use
    ID    string      `json:"id,omitempty"`
    Name  string      `json:"name,omitempty"`
    Input interface{} `json:"input,omitempty"`

    // tool_result
    ToolUseID string `json:"tool_use_id,omitempty"`
    Content   string `json:"content,omitempty"`
    IsError   bool   `json:"is_error,omitempty"`

    // thinking
    Thinking         string `json:"thinking,omitempty"`
    ThoughtSignature string `json:"thought_signature,omitempty"`

    // image (新增)
    ImageMIMEType string `json:"image_mime_type,omitempty"` // e.g. "image/jpeg"
    ImageData     string `json:"image_data,omitempty"`      // base64 encoded bytes
}
```

### 2. 图片缓存（新文件 `image_cache.go`）

```go
type ImageCache struct {
    mu    sync.RWMutex
    items map[string]cachedImage  // key: sandbox path
}

type cachedImage struct {
    MIMEType string
    Base64   string
}

func NewImageCache() *ImageCache
func (c *ImageCache) Get(path string) (cachedImage, bool)
func (c *ImageCache) Set(path, mimeType, base64 string)
```

- 挂载在 `AgentDeps.ImageCache *ImageCache`
- 进程生命周期内有效；重启后缓存清空（可接受，会触发重新下载）

### 3. 业务层变更（`harness.go`）

**`buildMessages` 签名**：

```go
func buildMessages(events []Event, imgCache *ImageCache) []llm.ClaudeMessage
```

处理 `user_message` 事件时：
- 有文字 → `{ type: "text", text: "..." }`
- 图片附件（`IsImage == true`）→ 查缓存：
  - 命中 → `{ type: "image", image_mime_type: "...", image_data: "<base64>" }`
  - 未命中 → `{ type: "text", text: "文件名 → 沙箱路径: ..." }`（降级）
- 非图片附件 → 文本描述（不变）

**`RunAgentWithContent` 预热缓存**：

在主 agent 循环前，对 `userContent.Attachments` 中 `IsImage == true` 的每个附件：
1. 查 `ImageCache`，命中则跳过
2. 调 `deps.Sandbox.DownloadFile(path)` 下载字节
3. `io.ReadAll` + `base64.StdEncoding.EncodeToString`
4. 写入缓存

下载失败时记录 warn 日志，不中断流程（后续 `buildMessages` 会降级到文本）。

### 4. LLM Provider 层序列化

各 provider 在将 `ContentBlock` 转为 API 请求格式时处理 `type == "image"`：

**Claude（`llm/claude.go`）**：
```json
{
  "type": "image",
  "source": {
    "type": "base64",
    "media_type": "image/jpeg",
    "data": "<base64>"
  }
}
```

**OpenAI（`llm/openai.go`）**：
```json
{
  "type": "image_url",
  "image_url": {
    "url": "data:image/jpeg;base64,<base64>"
  }
}
```

**Gemini（`llm/gemini.go`）**：

Gemini parts 中新增 `inlineData`：
```json
{
  "inlineData": {
    "mimeType": "image/jpeg",
    "data": "<base64>"
  }
}
```

三个 provider 的 `CallStream` 接口签名不变。

## 数据流

```
前端上传图片
    ↓
POST /api/sessions/{id}/upload
    → 写入沙箱 /home/gem/uploads/<filename>
    → 返回 { path, filename, mime_type, is_image: true }
    ↓
前端发消息（附带 attachments 元数据）
POST /api/sessions/{id}/messages
    ↓
RunAgentWithContent
    → 预热 ImageCache：下载图片字节 → base64 → 存缓存
    → EmitEvent(user_message, { text, attachments })
    ↓
Agent 主循环（每轮）
    → buildMessages(events, imgCache)
        → user_message 事件 → image content block（缓存命中）
                                或文本降级（缓存 miss）
    → CallStream(system, messages, tools, cb)
        → provider 序列化 image block → API 请求
    → LLM 直接处理图片内容
```

## 错误处理

| 场景 | 处理方式 |
|------|----------|
| 沙箱下载失败 | warn 日志，降级为文本路径 |
| 缓存 miss（进程重启后） | 同上，降级为文本路径 |
| provider 不支持图片格式 | API 返回错误，走现有 error 处理链路 |
| 图片过大（base64 超 token 限制） | 由 LLM API 报错，走现有 error 处理链路 |

## 涉及文件

| 文件 | 变更类型 |
|------|---------|
| `llm/client.go` | 修改：`ContentBlock` 新增 `ImageMIMEType`、`ImageData` |
| `llm/claude.go` | 修改：消息序列化支持 `image` block |
| `llm/openai.go` | 修改：消息序列化支持 `image_url` block |
| `llm/gemini.go` | 修改：parts 序列化支持 `inlineData` |
| `image_cache.go` | 新增：`ImageCache` 实现 |
| `harness.go` | 修改：`buildMessages` 签名 + 图片 block 生成；`RunAgentWithContent` 预热缓存；`AgentDeps` 新增 `ImageCache` |
| `main.go` | 修改：初始化 `ImageCache` 注入 `AgentDeps` |
