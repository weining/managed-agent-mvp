# Image Vision 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 图片上传后由后端读取字节、base64 编码，以原生 image content block 形式传入 LLM，让模型直接"看到"图片，支持 Claude / OpenAI / Gemini 三个 provider。

**Architecture:** 新增内存 `ImageCache`（按沙箱路径缓存 base64）挂载在 `AgentDeps`；`buildMessages` 接收 cache，遇到图片附件时生成 `image` content block；各 provider 各自序列化 image block 为各自 API 格式；cache miss 降级为文本路径描述。

**Tech Stack:** Go 标准库（`encoding/base64`, `sync`, `io`）；无新外部依赖。

---

## 文件总览

| 文件 | 变更 |
|------|------|
| `llm/client.go` | 修改：`ContentBlock` 新增 `ImageMIMEType`/`ImageData`，实现 `MarshalJSON` |
| `llm/client_test.go` | 新增：`ContentBlock` MarshalJSON 单元测试 |
| `llm/openai.go` | 修改：user message 转换支持 `image` block |
| `llm/openai_test.go` | 新增：OpenAI image 序列化单元测试 |
| `llm/gemini.go` | 修改：`geminiPart` 新增 `InlineData`，user turn 支持 image |
| `image_cache.go` | 新增：`ImageCache` 结构体与方法 |
| `image_cache_test.go` | 新增：`ImageCache` 单元测试 |
| `harness.go` | 修改：`buildMessages` 签名 + image block 生成；`RunAgentWithContent` 预热缓存；`AgentDeps` 新增字段 |
| `harness_test.go` | 新增：`buildMessages` image block 单元测试 |
| `main.go` | 修改：初始化 `ImageCache` 注入 `AgentDeps` |

---

### Task 1: ContentBlock 图片字段 + MarshalJSON

**Files:**
- Modify: `llm/client.go`
- Create: `llm/client_test.go`

- [ ] **Step 1: 在 `ContentBlock` 新增图片字段**

在 `llm/client.go` 的 `ContentBlock` 结构体末尾新增（在 `ThoughtSignature` 字段之后）：

```go
// image content block
ImageMIMEType string `json:"image_mime_type,omitempty"` // e.g. "image/jpeg"
ImageData     string `json:"image_data,omitempty"`      // base64 encoded bytes
```

- [ ] **Step 2: 添加必要 import**

确保 `llm/client.go` import 中有 `"encoding/json"`（已有）和 `"fmt"`（检查是否已有，已有则跳过）。

- [ ] **Step 3: 实现 ContentBlock.MarshalJSON**

在 `llm/client.go` 文件末尾添加（在 `Usage` 结构体定义之后）：

```go
// MarshalJSON produces Anthropic-compatible JSON for image blocks.
// For all other block types, the default struct marshaling is used.
func (b ContentBlock) MarshalJSON() ([]byte, error) {
	if b.Type == "image" {
		return json.Marshal(struct {
			Type   string `json:"type"`
			Source struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			} `json:"source"`
		}{
			Type: "image",
			Source: struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			}{
				Type:      "base64",
				MediaType: b.ImageMIMEType,
				Data:      b.ImageData,
			},
		})
	}
	type alias ContentBlock // avoid infinite recursion
	return json.Marshal(alias(b))
}
```

- [ ] **Step 4: 写失败测试**

创建 `llm/client_test.go`：

```go
package llm

import (
	"encoding/json"
	"testing"
)

func TestContentBlockMarshalJSON_Image(t *testing.T) {
	b := ContentBlock{
		Type:          "image",
		ImageMIMEType: "image/jpeg",
		ImageData:     "abc123==",
	}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if out["type"] != "image" {
		t.Errorf("type: got %v, want image", out["type"])
	}
	src, ok := out["source"].(map[string]interface{})
	if !ok {
		t.Fatalf("source field missing or wrong type: %v", out)
	}
	if src["type"] != "base64" {
		t.Errorf("source.type: got %v, want base64", src["type"])
	}
	if src["media_type"] != "image/jpeg" {
		t.Errorf("source.media_type: got %v, want image/jpeg", src["media_type"])
	}
	if src["data"] != "abc123==" {
		t.Errorf("source.data: got %v, want abc123==", src["data"])
	}
	// Must NOT contain image_mime_type or image_data at top level
	if _, exists := out["image_mime_type"]; exists {
		t.Error("image_mime_type should not appear at top level for image blocks")
	}
}

func TestContentBlockMarshalJSON_Text(t *testing.T) {
	b := ContentBlock{Type: "text", Text: "hello"}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var out map[string]interface{}
	json.Unmarshal(data, &out)
	if out["type"] != "text" {
		t.Errorf("type: got %v, want text", out["type"])
	}
	if out["text"] != "hello" {
		t.Errorf("text: got %v, want hello", out["text"])
	}
	// image fields must not appear
	if _, exists := out["source"]; exists {
		t.Error("source should not appear in text blocks")
	}
}

func TestContentBlockMarshalJSON_ToolResult(t *testing.T) {
	b := ContentBlock{
		Type:      "tool_result",
		ToolUseID: "tu_123",
		Content:   "result content",
		IsError:   false,
	}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var out map[string]interface{}
	json.Unmarshal(data, &out)
	if out["type"] != "tool_result" {
		t.Errorf("type: got %v, want tool_result", out["type"])
	}
	if out["tool_use_id"] != "tu_123" {
		t.Errorf("tool_use_id: got %v, want tu_123", out["tool_use_id"])
	}
}
```

- [ ] **Step 5: 运行测试确认失败（MarshalJSON 未实现时）**

```bash
cd /Users/xueweining/code/go/baidu/newapp/managed-agent-mvp
go test ./llm/ -run TestContentBlock -v
```

预期：Step 3 已实现，测试应 PASS。如果有编译错误，先修复。

- [ ] **Step 6: 确认测试通过**

```bash
go test ./llm/ -run TestContentBlock -v
```

预期输出包含 `PASS`。

- [ ] **Step 7: Commit**

```bash
git add llm/client.go llm/client_test.go
git commit -m "feat: add image content block support to ContentBlock with Anthropic-compatible MarshalJSON"
```

---

### Task 2: ImageCache 实现

**Files:**
- Create: `image_cache.go`
- Create: `image_cache_test.go`

- [ ] **Step 1: 写失败测试**

创建 `image_cache_test.go`：

```go
package main

import "testing"

func TestImageCache_GetSet(t *testing.T) {
	c := NewImageCache()

	// Get on empty cache
	if _, ok := c.Get("/some/path.jpg"); ok {
		t.Error("expected cache miss on empty cache")
	}

	// Set and Get
	c.Set("/some/path.jpg", "image/jpeg", "base64data==")
	item, ok := c.Get("/some/path.jpg")
	if !ok {
		t.Fatal("expected cache hit after Set")
	}
	if item.MIMEType != "image/jpeg" {
		t.Errorf("MIMEType: got %q, want image/jpeg", item.MIMEType)
	}
	if item.Base64 != "base64data==" {
		t.Errorf("Base64: got %q, want base64data==", item.Base64)
	}

	// Different path is still a miss
	if _, ok := c.Get("/other/path.png"); ok {
		t.Error("expected cache miss for different path")
	}
}

func TestImageCache_Overwrite(t *testing.T) {
	c := NewImageCache()
	c.Set("/img.jpg", "image/jpeg", "first")
	c.Set("/img.jpg", "image/jpeg", "second")
	item, _ := c.Get("/img.jpg")
	if item.Base64 != "second" {
		t.Errorf("expected overwrite: got %q, want second", item.Base64)
	}
}
```

- [ ] **Step 2: 运行确认编译失败**

```bash
go test -run TestImageCache -v
```

预期：编译失败，`NewImageCache` 未定义。

- [ ] **Step 3: 实现 ImageCache**

创建 `image_cache.go`：

```go
package main

import "sync"

// ImageCache holds base64-encoded image data keyed by sandbox path.
// It is safe for concurrent use and lives for the process lifetime.
type ImageCache struct {
	mu    sync.RWMutex
	items map[string]cachedImage
}

type cachedImage struct {
	MIMEType string
	Base64   string
}

// NewImageCache returns an empty ImageCache.
func NewImageCache() *ImageCache {
	return &ImageCache{items: make(map[string]cachedImage)}
}

// Get returns the cached image for path, if present.
func (c *ImageCache) Get(path string) (cachedImage, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	item, ok := c.items[path]
	return item, ok
}

// Set stores the image data for path, overwriting any existing entry.
func (c *ImageCache) Set(path, mimeType, base64Data string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[path] = cachedImage{MIMEType: mimeType, Base64: base64Data}
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
go test -run TestImageCache -v
```

预期：`PASS`。

- [ ] **Step 5: Commit**

```bash
git add image_cache.go image_cache_test.go
git commit -m "feat: add ImageCache for in-memory image base64 caching"
```

---

### Task 3: OpenAI image block 序列化

**Files:**
- Modify: `llm/openai.go`
- Create: `llm/openai_test.go`

- [ ] **Step 1: 写失败测试**

创建 `llm/openai_test.go`：

```go
package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// oaiRequestCapture 辅助函数：从 CallStream 拿不到 body，通过 helper 测试序列化逻辑
// 我们直接测 buildOAIMessages（将在 Step 3 提取）
func TestBuildOAIMessages_ImageBlock(t *testing.T) {
	msgs := []ClaudeMessage{
		{
			Role: "user",
			Content: []ContentBlock{
				{Type: "text", Text: "describe this"},
				{Type: "image", ImageMIMEType: "image/png", ImageData: "abc=="},
			},
		},
	}

	oaiMsgs := buildOAIMessages("", msgs)

	// Should produce one user message with array content
	var userMsg *oaiMessage
	for i := range oaiMsgs {
		if oaiMsgs[i].Role == "user" {
			userMsg = &oaiMsgs[i]
			break
		}
	}
	if userMsg == nil {
		t.Fatal("no user message found")
	}

	// Content must be []interface{} (array), not string
	parts, ok := userMsg.Content.([]interface{})
	if !ok {
		t.Fatalf("expected array content, got %T: %v", userMsg.Content, userMsg.Content)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}

	// First part: text
	textPart := parts[0].(map[string]interface{})
	if textPart["type"] != "text" {
		t.Errorf("part[0].type: got %v, want text", textPart["type"])
	}

	// Second part: image_url
	imgPart := parts[1].(map[string]interface{})
	if imgPart["type"] != "image_url" {
		t.Errorf("part[1].type: got %v, want image_url", imgPart["type"])
	}
	imgURL := imgPart["image_url"].(map[string]interface{})
	url, _ := imgURL["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Errorf("image_url.url: got %q, expected data:image/png;base64, prefix", url)
	}
	if !strings.HasSuffix(url, "abc==") {
		t.Errorf("image_url.url: got %q, expected abc== suffix", url)
	}
}

func TestBuildOAIMessages_TextOnly(t *testing.T) {
	msgs := []ClaudeMessage{
		{
			Role:    "user",
			Content: []ContentBlock{{Type: "text", Text: "hello"}},
		},
	}
	oaiMsgs := buildOAIMessages("", msgs)
	for _, m := range oaiMsgs {
		if m.Role == "user" {
			// content should be plain string (not array) when no images
			if _, ok := m.Content.(string); !ok {
				t.Errorf("expected string content for text-only, got %T", m.Content)
			}
		}
	}
}

// ensure json round-trip doesn't break
func TestBuildOAIMessages_JSONRoundtrip(t *testing.T) {
	msgs := []ClaudeMessage{
		{
			Role: "user",
			Content: []ContentBlock{
				{Type: "image", ImageMIMEType: "image/jpeg", ImageData: "data"},
			},
		},
	}
	oaiMsgs := buildOAIMessages("", msgs)
	_, err := json.Marshal(oaiMsgs)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
}
```

- [ ] **Step 2: 运行确认编译失败**

```bash
go test ./llm/ -run TestBuildOAIMessages -v
```

预期：编译失败，`buildOAIMessages` 未定义。

- [ ] **Step 3: 提取并修改 buildOAIMessages**

在 `llm/openai.go` 中，将 `CallStream` 里构建 `oaiMsgs` 的逻辑提取为独立函数，并添加 image 支持。

在 `CallStream` 函数开头找到：
```go
var oaiMsgs []oaiMessage
if system != "" {
    oaiMsgs = append(oaiMsgs, oaiMessage{Role: "system", Content: system})
}

for _, msg := range messages {
    switch msg.Role {
    case "user":
        ...
    case "assistant":
        ...
    }
}
```

将这段逻辑替换为调用 `buildOAIMessages`，并在 `CallStream` 之前（或之后）添加函数定义：

```go
// buildOAIMessages converts canonical ClaudeMessages to OpenAI message format.
// Image content blocks become image_url parts with base64 data URLs.
func buildOAIMessages(system string, messages []ClaudeMessage) []oaiMessage {
	var oaiMsgs []oaiMessage
	if system != "" {
		oaiMsgs = append(oaiMsgs, oaiMessage{Role: "system", Content: system})
	}

	for _, msg := range messages {
		switch msg.Role {
		case "user":
			var toolResults []ContentBlock
			var contentParts []interface{}
			hasImage := false

			for _, block := range msg.Content {
				switch block.Type {
				case "tool_result":
					toolResults = append(toolResults, block)
				case "image":
					hasImage = true
					contentParts = append(contentParts, map[string]interface{}{
						"type": "image_url",
						"image_url": map[string]interface{}{
							"url": "data:" + block.ImageMIMEType + ";base64," + block.ImageData,
						},
					})
				case "text":
					if hasImage {
						// already building array parts
						contentParts = append(contentParts, map[string]interface{}{
							"type": "text",
							"text": block.Text,
						})
					} else {
						// may still become array if a later block is image;
						// store as placeholder for now
						contentParts = append(contentParts, map[string]interface{}{
							"type": "text",
							"text": block.Text,
						})
					}
				}
			}

			for _, tr := range toolResults {
				oaiMsgs = append(oaiMsgs, oaiMessage{
					Role:       "tool",
					ToolCallID: tr.ToolUseID,
					Content:    tr.Content,
				})
			}

			if len(contentParts) > 0 {
				// Check if any image blocks present
				anyImage := false
				for _, p := range contentParts {
					if pm, ok := p.(map[string]interface{}); ok {
						if pm["type"] == "image_url" {
							anyImage = true
							break
						}
					}
				}
				if anyImage {
					oaiMsgs = append(oaiMsgs, oaiMessage{Role: "user", Content: contentParts})
				} else {
					// No images: join text parts as plain string
					var texts []string
					for _, p := range contentParts {
						if pm, ok := p.(map[string]interface{}); ok {
							if t, ok := pm["text"].(string); ok {
								texts = append(texts, t)
							}
						}
					}
					oaiMsgs = append(oaiMsgs, oaiMessage{
						Role:    "user",
						Content: strings.Join(texts, "\n"),
					})
				}
			}

		case "assistant":
			var textParts []string
			var toolCalls []oaiToolCall
			for _, block := range msg.Content {
				if block.Type == "text" && block.Text != "" {
					textParts = append(textParts, block.Text)
				} else if block.Type == "tool_use" {
					argsBytes, _ := json.Marshal(block.Input)
					toolCalls = append(toolCalls, oaiToolCall{
						ID:   block.ID,
						Type: "function",
						Function: oaiToolFunction{
							Name:      block.Name,
							Arguments: string(argsBytes),
						},
					})
				}
			}
			var content interface{}
			if len(textParts) > 0 {
				content = strings.Join(textParts, "\n")
			}
			oaiMsgs = append(oaiMsgs, oaiMessage{
				Role:      "assistant",
				Content:   content,
				ToolCalls: toolCalls,
			})
		}
	}
	return oaiMsgs
}
```

然后在 `CallStream` 中将原来的 `oaiMsgs` 构建替换为：

```go
oaiMsgs := buildOAIMessages(system, messages)
```

（删除 `CallStream` 中原来那段 `var oaiMsgs []oaiMessage ... for _, msg := range messages { ... }` 的代码，替换为这一行）

- [ ] **Step 4: 运行测试确认通过**

```bash
go test ./llm/ -run TestBuildOAIMessages -v
```

预期：`PASS`。

- [ ] **Step 5: 确认全量编译通过**

```bash
go build ./...
```

- [ ] **Step 6: Commit**

```bash
git add llm/openai.go llm/openai_test.go
git commit -m "feat: add image_url block support to OpenAI provider"
```

---

### Task 4: Gemini image block 序列化

**Files:**
- Modify: `llm/gemini.go`

- [ ] **Step 1: 添加 geminiInlineData 类型**

在 `llm/gemini.go` 的 `geminiPart` 结构体前添加：

```go
type geminiInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"` // base64
}
```

- [ ] **Step 2: 在 geminiPart 中添加 InlineData 字段**

在 `geminiPart` 结构体中，在 `Text` 字段之后添加：

```go
InlineData *geminiInlineData `json:"inlineData,omitempty"`
```

修改后的 `geminiPart` 结构体：

```go
type geminiPart struct {
	Text               string              `json:"text,omitempty"`
	InlineData         *geminiInlineData   `json:"inlineData,omitempty"`
	FunctionCall       *geminiFunctionCall `json:"functionCall,omitempty"`
	FunctionResp       *geminiFunctionResp `json:"functionResponse,omitempty"`
	ThoughtSignature   string              `json:"thoughtSignature,omitempty"`
	ThoughtSignatureSC string              `json:"thought_signature,omitempty"`
}
```

- [ ] **Step 3: 在 user turn 构建中处理 image block**

在 `CallStream` 的 `role == "user"` 分支中，找到：

```go
switch block.Type {
case "text":
    textParts = append(textParts, geminiPart{Text: block.Text})
case "tool_result":
    ...
}
```

在 `case "text":` 之后添加：

```go
case "image":
    textParts = append(textParts, geminiPart{
        InlineData: &geminiInlineData{
            MIMEType: block.ImageMIMEType,
            Data:     block.ImageData,
        },
    })
```

- [ ] **Step 4: 确认编译通过**

```bash
go build ./...
```

预期：无错误。

- [ ] **Step 5: Commit**

```bash
git add llm/gemini.go
git commit -m "feat: add inlineData image block support to Gemini provider"
```

---

### Task 5: buildMessages 支持 image content block

**Files:**
- Modify: `harness.go`
- Create: `harness_test.go`

- [ ] **Step 1: 写失败测试**

创建 `harness_test.go`：

```go
package main

import (
	"testing"

	"managed-agent/llm"
)

func TestBuildMessages_ImageBlock(t *testing.T) {
	cache := NewImageCache()
	cache.Set("/home/gem/uploads/photo.jpg", "image/jpeg", "base64abc==")

	events := []Event{
		{
			Type: "user_message",
			Content: map[string]interface{}{
				"text": "what is this?",
				"attachments": []interface{}{
					map[string]interface{}{
						"path":      "/home/gem/uploads/photo.jpg",
						"filename":  "photo.jpg",
						"mime_type": "image/jpeg",
						"is_image":  true,
					},
				},
			},
		},
	}

	msgs := buildMessages(events, cache)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	msg := msgs[0]
	if msg.Role != "user" {
		t.Errorf("role: got %q, want user", msg.Role)
	}

	// Expect: text block + image block
	var textBlocks, imageBlocks []llm.ContentBlock
	for _, b := range msg.Content {
		switch b.Type {
		case "text":
			textBlocks = append(textBlocks, b)
		case "image":
			imageBlocks = append(imageBlocks, b)
		}
	}

	if len(textBlocks) != 1 {
		t.Errorf("expected 1 text block, got %d", len(textBlocks))
	} else if textBlocks[0].Text != "what is this?" {
		t.Errorf("text: got %q, want 'what is this?'", textBlocks[0].Text)
	}

	if len(imageBlocks) != 1 {
		t.Errorf("expected 1 image block, got %d", len(imageBlocks))
	} else {
		if imageBlocks[0].ImageMIMEType != "image/jpeg" {
			t.Errorf("ImageMIMEType: got %q, want image/jpeg", imageBlocks[0].ImageMIMEType)
		}
		if imageBlocks[0].ImageData != "base64abc==" {
			t.Errorf("ImageData: got %q, want base64abc==", imageBlocks[0].ImageData)
		}
	}
}

func TestBuildMessages_ImageCacheMiss_FallbackToText(t *testing.T) {
	cache := NewImageCache() // empty cache — cache miss

	events := []Event{
		{
			Type: "user_message",
			Content: map[string]interface{}{
				"text": "hello",
				"attachments": []interface{}{
					map[string]interface{}{
						"path":      "/home/gem/uploads/photo.jpg",
						"filename":  "photo.jpg",
						"mime_type": "image/jpeg",
						"is_image":  true,
					},
				},
			},
		},
	}

	msgs := buildMessages(events, cache)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	// All blocks must be text (image degraded to text on cache miss)
	for _, b := range msgs[0].Content {
		if b.Type == "image" {
			t.Error("expected no image blocks on cache miss, got one")
		}
	}
}

func TestBuildMessages_TextOnly(t *testing.T) {
	cache := NewImageCache()
	events := []Event{
		{Type: "user_message", Content: "plain text message"},
	}
	msgs := buildMessages(events, cache)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Content[0].Text != "plain text message" {
		t.Errorf("text: got %q, want 'plain text message'", msgs[0].Content[0].Text)
	}
}
```

- [ ] **Step 2: 运行确认编译失败**

```bash
go test -run TestBuildMessages -v
```

预期：编译失败，`buildMessages` 签名不匹配（当前只接收 `[]Event`）。

- [ ] **Step 3: 修改 buildMessages 签名**

在 `harness.go` 中，将：

```go
func buildMessages(events []Event) []llm.ClaudeMessage {
```

改为：

```go
func buildMessages(events []Event, imgCache *ImageCache) []llm.ClaudeMessage {
```

- [ ] **Step 4: 修改 user_message case 中的内容构建**

在 `buildMessages` 中找到 `user_message` case：

```go
case "user_message":
    flushAssistant()
    flushToolResults()
    text := extractUserMessageText(evt.Content)
    messages = append(messages, llm.ClaudeMessage{
        Role: "user",
        Content: []llm.ContentBlock{
            {Type: "text", Text: text},
        },
    })
```

将其替换为：

```go
case "user_message":
    flushAssistant()
    flushToolResults()
    blocks := extractUserMessageBlocks(evt.Content, imgCache)
    messages = append(messages, llm.ClaudeMessage{
        Role:    "user",
        Content: blocks,
    })
```

- [ ] **Step 5: 添加 extractUserMessageBlocks 和 buildContentBlocks**

在 `harness.go` 中找到现有的 `extractUserMessageText` 函数，在其**之后**添加：

```go
// extractUserMessageBlocks converts a user_message event's content into
// content blocks, substituting cached images as native image blocks.
func extractUserMessageBlocks(content interface{}, imgCache *ImageCache) []llm.ContentBlock {
	switch v := content.(type) {
	case string:
		if v == "" {
			return []llm.ContentBlock{{Type: "text", Text: ""}}
		}
		return []llm.ContentBlock{{Type: "text", Text: v}}
	default:
		data, _ := json.Marshal(v)
		var msg UserMessageContent
		if err := json.Unmarshal(data, &msg); err == nil {
			return buildContentBlocks(msg, imgCache)
		}
	}
	return []llm.ContentBlock{{Type: "text", Text: ""}}
}

// buildContentBlocks converts a UserMessageContent into content blocks.
// Image attachments with cache hits become image blocks; others become text.
func buildContentBlocks(msg UserMessageContent, imgCache *ImageCache) []llm.ContentBlock {
	var blocks []llm.ContentBlock

	if text := strings.TrimSpace(msg.Text); text != "" {
		blocks = append(blocks, llm.ContentBlock{Type: "text", Text: text})
	}

	var textAttachments []string
	for _, att := range msg.Attachments {
		if att.IsImage {
			if cached, ok := imgCache.Get(att.Path); ok {
				blocks = append(blocks, llm.ContentBlock{
					Type:          "image",
					ImageMIMEType: cached.MIMEType,
					ImageData:     cached.Base64,
				})
				continue
			}
			// cache miss: fall back to text description
		}
		// non-image or cache-miss: text description
		desc := att.Filename
		if att.MIMEType != "" {
			desc += " (" + att.MIMEType + ")"
		}
		desc += " → 沙箱路径: " + att.Path
		if att.IsImage {
			desc += " [图片]"
		}
		textAttachments = append(textAttachments, desc)
	}

	if len(textAttachments) > 0 {
		blocks = append(blocks, llm.ContentBlock{
			Type: "text",
			Text: "[用户已上传附件]\n- " + strings.Join(textAttachments, "\n- "),
		})
	}

	if len(blocks) == 0 {
		blocks = append(blocks, llm.ContentBlock{Type: "text", Text: ""})
	}
	return blocks
}
```

- [ ] **Step 6: 修复 RunAgentWithContent 中对 buildMessages 的调用**

在 `harness.go` 的 `RunAgentWithContent` 中找到：

```go
messages := buildMessages(sess.Events)
```

改为：

```go
messages := buildMessages(sess.Events, deps.ImageCache)
```

- [ ] **Step 7: 在 AgentDeps 中添加 ImageCache 字段**

在 `harness.go` 的 `AgentDeps` 结构体中添加：

```go
ImageCache *ImageCache
```

修改后：

```go
type AgentDeps struct {
	Store      *SessionStore
	Sandbox    *SDKSandboxClient
	Claude     llm.LLMClient
	Skills     *SkillRegistry
	Config     *Config
	ImageCache *ImageCache
}
```

- [ ] **Step 8: 确认编译通过**

```bash
go build ./...
```

如有编译错误，修复后再继续。

- [ ] **Step 9: 运行测试**

```bash
go test -run TestBuildMessages -v
```

预期：`PASS`。

- [ ] **Step 10: Commit**

```bash
git add harness.go harness_test.go
git commit -m "feat: buildMessages produces native image blocks for cached images"
```

---

### Task 6: RunAgentWithContent 图片预取

**Files:**
- Modify: `harness.go`

- [ ] **Step 1: 添加必要 import**

确认 `harness.go` 的 import 包含 `"encoding/base64"` 和 `"io"`。如不含，添加：

```go
"encoding/base64"
"io"
```

- [ ] **Step 2: 添加 prefetchSessionImages 函数**

在 `harness.go` 末尾添加：

```go
// prefetchSessionImages downloads all image attachments in session events
// that are not yet cached into imgCache. Cache misses cause download from sandbox.
// Errors are logged but do not abort the agent.
func prefetchSessionImages(events []Event, sbx *SDKSandboxClient, imgCache *ImageCache) {
	for _, evt := range events {
		if evt.Type != "user_message" {
			continue
		}
		data, _ := json.Marshal(evt.Content)
		var msg UserMessageContent
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		for _, att := range msg.Attachments {
			if !att.IsImage {
				continue
			}
			if _, ok := imgCache.Get(att.Path); ok {
				continue // already cached
			}
			reader, err := sbx.DownloadFile(att.Path)
			if err != nil {
				log.Printf("image prefetch: download failed for %s: %v", att.Path, err)
				continue
			}
			raw, err := io.ReadAll(reader)
			if err != nil {
				log.Printf("image prefetch: read failed for %s: %v", att.Path, err)
				continue
			}
			mimeType := att.MIMEType
			if mimeType == "" {
				mimeType = "image/jpeg"
			}
			imgCache.Set(att.Path, mimeType, base64.StdEncoding.EncodeToString(raw))
			log.Printf("image prefetch: cached %s (%d bytes)", att.Path, len(raw))
		}
	}
}
```

- [ ] **Step 3: 在 RunAgentWithContent 中调用预取**

在 `RunAgentWithContent` 函数中，找到：

```go
// 1. Emit user message event
store.EmitEvent(sess.ID, Event{
    Type:    "user_message",
    Content: userContent,
})
```

在其**之后**（emit 完成后，进入主循环之前）添加：

```go
// Prefetch all image attachments in session history into cache.
if deps.ImageCache != nil {
    freshSess, _ := store.Get(sess.ID)
    if freshSess != nil {
        prefetchSessionImages(freshSess.Events, deps.Sandbox, deps.ImageCache)
    }
}
```

- [ ] **Step 4: 确认编译通过**

```bash
go build ./...
```

- [ ] **Step 5: Commit**

```bash
git add harness.go
git commit -m "feat: prefetch session image attachments into ImageCache before agent loop"
```

---

### Task 7: main.go 初始化 ImageCache

**Files:**
- Modify: `main.go`

- [ ] **Step 1: 在 main.go 中初始化并注入 ImageCache**

在 `main.go` 的 `deps` 赋值处找到：

```go
deps := &AgentDeps{
    Store:   store,
    Sandbox: sbx,
    Claude:  llmClient,
    Skills:  skills,
    Config:  cfg,
}
```

替换为：

```go
deps := &AgentDeps{
    Store:      store,
    Sandbox:    sbx,
    Claude:     llmClient,
    Skills:     skills,
    Config:     cfg,
    ImageCache: NewImageCache(),
}
```

- [ ] **Step 2: 确认完整编译通过**

```bash
go build -o bin/managed-agent-mvp
```

预期：无错误，二进制生成。

- [ ] **Step 3: 运行全部单元测试**

```bash
go test -race -timeout=60s -v -run "TestContentBlock|TestImageCache|TestBuildMessages|TestBuildOAI" ./...
```

预期：所有测试 `PASS`，无 race condition。

- [ ] **Step 4: Commit**

```bash
git add main.go
git commit -m "feat: wire ImageCache into AgentDeps for image vision support"
```

---

## 验收标准

1. `go build ./...` 无错误
2. 所有单元测试通过（`go test -race ./...`）
3. 上传图片后发送消息，LLM 收到的请求中包含 `image` content block（可通过 `llm_debug: true` 验证日志）
4. 无图片或 cache miss 时，降级为文本路径，不报错
