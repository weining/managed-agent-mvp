package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"managed-agent/llm"
)

const baseSystemPrompt = `你是一个强大的 AI 助手，运行在一个功能丰富的 AIO (All-In-One) 沙箱环境中。

## 环境能力
- **系统**: Ubuntu 22.04, 32核 CPU, 125GB 内存, 2TB 磁盘
- **编程语言**: Python 3.10 (含 numpy/pandas/matplotlib/opencv/requests/fastapi/httpx/beautifulsoup4), Node.js 22, GCC 11
- **浏览器**: Chromium 135 (完整交互能力：导航、点击、填写、截图、执行JS)
- **工具**: git, jq, ImageMagick (convert), curl, wget
- **联网**: 完整外网访问，可以 pip install / npm install 安装额外包
- **显示**: Xvnc 虚拟桌面 1280x1024, 支持 GUI 应用和屏幕录制

## 工具选择策略

| 场景 | 使用工具 |
|------|---------|
| 运行命令、安装包、短时操作 | execute_command |
| 启动服务器、长时运行进程 | shell_session |
| 创建或完整覆盖文件 | write_file |
| 读取文件内容 | read_file |
| 精准编辑文件（推荐代码修改场景）| file_edit（view/str_replace/insert/undo_edit）|
| 列出目录内容 | search_files action=list |
| 搜索文件内容或按名查找文件 | search_files action=grep/find |
| 访问网页、提取文档内容（Markdown）| browse_web |
| 验证前端页面效果 | take_screenshot |
| 浏览器导航到 URL | browser_action action=navigate |
| 点击/填表单/按键/执行JS等交互 | browser_action（click/fill/type_text/press_key/evaluate）|
| 获取页面可交互元素 | browser_action action=get_elements |
| 交互式数据分析、逐步调试 | execute_code |
| 调用 MCP 服务 | mcp_tool（先 list_servers → list_tools → call_tool）|
| 录制桌面操作过程 | display_record |
| 生成文件交付用户下载 | download_file（必须主动调用，不要只告诉路径）|
| 解读或处理用户上传的文件 | 用 read_file 读取 /home/gem/uploads/ 下对应的文件 |

## 搜索引擎规则
- 中文资料: https://www.baidu.com/s?wd=关键词
- 英文资料: https://www.bing.com/search?q=keywords
- **禁止使用 Google**（沙箱无法访问）

## 行为准则
- 直接动手解决问题，先写代码再运行验证
- 遇到错误时分析原因并修复，不要只是报告错误
- 用中文回复用户，代码注释可以用英文
- 保持回复简洁，避免冗长解释
- 在沙箱中生成了用户需要下载的文件（文档、图片、压缩包等）后，**必须立即调用 download_file** 提供下载链接`

// SSEWriter is the interface for streaming events to the client.
type SSEWriter interface {
	WriteEvent(event, data string)
	Flush()
}

// AgentDeps groups the dependencies injected into RunAgent.
type AgentDeps struct {
	Store      *SessionStore
	Sandbox    *SDKSandboxClient
	Claude     llm.LLMClient
	Skills     *SkillRegistry
	Config     *Config
	ImageCache *ImageCache
}

// buildSystemPrompt composes the full system prompt from the base, skill
// summary, and any active skill prompts.
func buildSystemPrompt(skills *SkillRegistry, activeSkills []string, skillArgs map[string]string) string {
	var sb strings.Builder
	sb.WriteString(baseSystemPrompt)
	sb.WriteString("\n\n## 当前时间\n")
	sb.WriteString(time.Now().Format("2006-01-02 15:04:05 (Monday)"))
	sb.WriteString("\n")
	sb.WriteString(skills.SkillSummary())
	sb.WriteString(skills.ActiveSkillsPrompt(activeSkills, skillArgs))
	return sb.String()
}

// RunAgent executes the agent loop for a user message in a session.
func RunAgent(deps *AgentDeps, sess *Session, userMsg string, sse SSEWriter) error {
	return RunAgentWithContent(deps, sess, UserMessageContent{Text: userMsg}, sse)
}

func RunAgentWithContent(deps *AgentDeps, sess *Session, userContent UserMessageContent, sse SSEWriter) error {
	store := deps.Store

	// 1. Emit user message event
	store.EmitEvent(sess.ID, Event{
		Type:    "user_message",
		Content: userContent,
	})

	// Prefetch all image attachments in session history into cache.
	if deps.ImageCache != nil {
		freshSess, _ := store.Get(sess.ID)
		if freshSess != nil {
			prefetchSessionImages(freshSess.Events, deps.Sandbox, deps.ImageCache)
		}
	}

	hasSkills := len(deps.Skills.List()) > 0
	tools := ToolDefinitions(hasSkills)

	maxRounds := 50
	if deps.Config != nil {
		if n, err := strconv.Atoi(deps.Config.MaxLoopRounds); err == nil && n > 0 {
			maxRounds = n
		}
	}

	for round := 0; round < maxRounds; round++ {
		// Reload session to get latest events (including skill changes)
		sess, _ = store.Get(sess.ID)

		systemPrompt := buildSystemPrompt(deps.Skills, sess.ActiveSkills, sess.SkillArgs)
		messages := buildMessages(sess.Events, deps.ImageCache)

		// 3. Call Claude (streaming)
		var fullText string
		resp, err := deps.Claude.CallStream(systemPrompt, messages, tools, func(eventType string, data interface{}) {
			switch eventType {
			case "text":
				text, _ := data.(string)
				fullText += text
				jsonData, _ := json.Marshal(map[string]string{"content": text})
				sse.WriteEvent("text", string(jsonData))
				sse.Flush()
			case "thinking_start":
				sse.WriteEvent("thinking_start", "{}")
				sse.Flush()
			case "thinking":
				text, _ := data.(string)
				jsonData, _ := json.Marshal(map[string]string{"content": text})
				sse.WriteEvent("thinking", string(jsonData))
				sse.Flush()
			case "tool_use_start":
				jsonData, _ := json.Marshal(data)
				sse.WriteEvent("tool_use_start", string(jsonData))
				sse.Flush()
			case "tool_use":
				jsonData, _ := json.Marshal(data)
				sse.WriteEvent("tool_use", string(jsonData))
				sse.Flush()
			}
		})

		if err != nil {
			log.Printf("Claude API error: %v", err)
			jsonData, _ := json.Marshal(map[string]string{"error": err.Error()})
			sse.WriteEvent("error", string(jsonData))
			sse.Flush()
			return err
		}

		// 4. Process response content blocks
		hasToolUse := false
		var toolResults []llm.ContentBlock

		for _, block := range resp.Content {
			switch block.Type {
			case "text":
				store.EmitEvent(sess.ID, Event{
					Type:    "assistant_message",
					Content: block.Text,
				})

			case "tool_use":
				hasToolUse = true
				inputMap := toInputMap(block.Input)

				evtContent := map[string]interface{}{
					"id":    block.ID,
					"name":  block.Name,
					"input": inputMap,
				}
				if block.ThoughtSignature != "" {
					evtContent["thought_signature"] = block.ThoughtSignature
				}
				store.EmitEvent(sess.ID, Event{
					Type:    "tool_use",
					Content: evtContent,
				})

				// Dispatch: skill tool is handled locally, everything else goes to sandbox
				var output string
				var isError bool
				if block.Name == "skill" {
					output, isError = executeSkillTool(deps, sess.ID, inputMap)
				} else {
					output, isError = ExecuteTool(deps.Sandbox, block.Name, inputMap)
				}

				// Send tool result to frontend
				jsonData, _ := json.Marshal(map[string]interface{}{
					"tool_use_id": block.ID,
					"name":        block.Name,
					"input":       inputMap,
					"output":      output,
					"is_error":    isError,
				})
				sse.WriteEvent("tool_result", string(jsonData))
				sse.Flush()

				store.EmitEvent(sess.ID, Event{
					Type: "tool_result",
					Content: map[string]interface{}{
						"tool_use_id": block.ID,
						"output":      output,
						"is_error":    isError,
					},
				})

				toolResults = append(toolResults, llm.ContentBlock{
					Type:      "tool_result",
					ToolUseID: block.ID,
					Content:   output,
					IsError:   isError,
				})
			}
		}

		// 5. If no tool_use, we're done
		if !hasToolUse {
			sse.WriteEvent("done", "{}")
			sse.Flush()
			return nil
		}

		// 6. Continue loop - tool results will be picked up from session events
		log.Printf("Agent loop round %d: executed %d tools, continuing...", round+1, len(toolResults))
	}

	// Hit max rounds
	sse.WriteEvent("error", `{"error":"Agent loop exceeded maximum rounds"}`)
	sse.Flush()
	return fmt.Errorf("agent loop exceeded %d rounds", maxRounds)
}

func buildUserPrompt(content UserMessageContent) string {
	text := strings.TrimSpace(content.Text)
	if len(content.Attachments) == 0 {
		return text
	}

	var sb strings.Builder
	if text != "" {
		sb.WriteString(text)
		sb.WriteString("\n\n")
	}
	sb.WriteString("[用户已上传附件]\n")
	for _, attachment := range content.Attachments {
		sb.WriteString("- ")
		sb.WriteString(attachment.Filename)
		if attachment.MIMEType != "" {
			sb.WriteString(" (")
			sb.WriteString(attachment.MIMEType)
			sb.WriteString(")")
		}
		sb.WriteString(" → 沙箱路径: ")
		sb.WriteString(attachment.Path)
		if attachment.IsImage {
			sb.WriteString(" [图片]")
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

// executeSkillTool handles the "skill" tool calls (activate/deactivate/list).
func executeSkillTool(deps *AgentDeps, sessionID string, input map[string]interface{}) (string, bool) {
	action, _ := input["action"].(string)
	registry := deps.Skills
	store := deps.Store

	switch action {
	case "list":
		skills := registry.ListModelInvocable()
		if len(skills) == 0 {
			return "没有可用的 skill。", false
		}
		var sb strings.Builder
		sb.WriteString("可用 skills:\n")
		for _, s := range skills {
			fmt.Fprintf(&sb, "- **%s**: %s", s.Name, s.Description)
			if s.HasFiles() {
				fmt.Fprintf(&sb, " [含 %d 个文件]", len(s.Files))
			}
			sb.WriteString("\n")
		}
		return sb.String(), false

	case "activate":
		name, _ := input["name"].(string)
		if name == "" {
			return "Error: name is required for activate", true
		}
		skill := registry.Get(name)
		if skill == nil {
			return fmt.Sprintf("Error: skill %q not found", name), true
		}
		sess, err := store.Get(sessionID)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		for _, s := range sess.ActiveSkills {
			if s == name {
				return fmt.Sprintf("Skill %q is already active.", name), false
			}
		}
		if err := store.SetActiveSkills(sessionID, append(sess.ActiveSkills, name)); err != nil {
			return "Error: " + err.Error(), true
		}

		// Deploy files to sandbox if present
		var fileInfo string
		if skill.HasFiles() {
			deployed, deployErr := deploySkillFiles(deps.Sandbox, skill)
			if deployErr != nil {
				fileInfo = fmt.Sprintf("\nWarning: failed to deploy files: %v", deployErr)
			} else {
				fileInfo = fmt.Sprintf("\n%d file(s) deployed to %s: %s",
					len(skill.Files), skill.SandboxDir(), strings.Join(deployed, ", "))
			}
		}

		return fmt.Sprintf("Skill %q activated. Follow its instructions below.%s", name, fileInfo), false

	case "deactivate":
		name, _ := input["name"].(string)
		if name == "" {
			return "Error: name is required for deactivate", true
		}
		sess, err := store.Get(sessionID)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		var remaining []string
		found := false
		for _, s := range sess.ActiveSkills {
			if s == name {
				found = true
			} else {
				remaining = append(remaining, s)
			}
		}
		if !found {
			return fmt.Sprintf("Skill %q is not active.", name), false
		}
		if err := store.SetActiveSkills(sessionID, remaining); err != nil {
			return "Error: " + err.Error(), true
		}
		return fmt.Sprintf("Skill %q deactivated.", name), false

	default:
		return "Error: invalid skill action: " + action, true
	}
}

// deploySkillFiles uploads a skill's bundled files to the sandbox.
// It creates subdirectories as needed and makes scripts executable.
func deploySkillFiles(sbx *SDKSandboxClient, skill *Skill) ([]string, error) {
	dir := skill.SandboxDir()

	// Create the root directory
	stdout, exitCode, err := sbx.ExecCommand("mkdir -p " + dir)
	if err != nil {
		return nil, fmt.Errorf("failed to create dir %s: %w", dir, err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("mkdir -p %s failed (exit %d): %s", dir, exitCode, stdout)
	}

	// Track which subdirectories we've already created
	createdDirs := map[string]bool{dir: true}
	var deployed []string

	for _, f := range skill.Files {
		targetPath := dir + "/" + f.RelPath

		// Ensure parent directory exists
		parentDir := filepath.Dir(targetPath)
		if !createdDirs[parentDir] {
			stdout, exitCode, err = sbx.ExecCommand("mkdir -p " + parentDir)
			if err != nil {
				return deployed, fmt.Errorf("failed to create dir %s: %w", parentDir, err)
			}
			if exitCode != 0 {
				return deployed, fmt.Errorf("mkdir -p %s failed (exit %d): %s", parentDir, exitCode, stdout)
			}
			createdDirs[parentDir] = true
		}

		if err := sbx.WriteFile(targetPath, f.Content); err != nil {
			return deployed, fmt.Errorf("failed to write %s: %w", targetPath, err)
		}

		// Make scripts executable (files in scripts/ or with common script extensions)
		if isExecutable(f.RelPath) {
			sbx.ExecCommand("chmod +x " + targetPath)
		}

		deployed = append(deployed, f.RelPath)
	}
	return deployed, nil
}

// isExecutable returns true if the file looks like it should be executable.
func isExecutable(relPath string) bool {
	if strings.HasPrefix(relPath, "scripts/") || strings.HasPrefix(relPath, "scripts\\") {
		return true
	}
	ext := strings.ToLower(filepath.Ext(relPath))
	switch ext {
	case ".sh", ".bash", ".py", ".rb", ".pl", ".js":
		return true
	}
	return false
}

// buildMessages converts session events into Claude message format.
func buildMessages(events []Event, imgCache *ImageCache) []llm.ClaudeMessage {
	var messages []llm.ClaudeMessage
	var pendingAssistant []llm.ContentBlock
	var pendingToolResults []llm.ContentBlock

	flushAssistant := func() {
		if len(pendingAssistant) > 0 {
			messages = append(messages, llm.ClaudeMessage{
				Role:    "assistant",
				Content: pendingAssistant,
			})
			pendingAssistant = nil
		}
	}

	flushToolResults := func() {
		if len(pendingToolResults) > 0 {
			messages = append(messages, llm.ClaudeMessage{
				Role:    "user",
				Content: pendingToolResults,
			})
			pendingToolResults = nil
		}
	}

	for _, evt := range events {
		switch evt.Type {
		case "user_message":
			flushAssistant()
			flushToolResults()
			blocks := extractUserMessageBlocks(evt.Content, imgCache)
			messages = append(messages, llm.ClaudeMessage{
				Role:    "user",
				Content: blocks,
			})

		case "assistant_message":
			flushToolResults()
			text, _ := evt.Content.(string)
			pendingAssistant = append(pendingAssistant, llm.ContentBlock{
				Type: "text",
				Text: text,
			})

		case "tool_use":
			flushToolResults()
			m := toStringMap(evt.Content)
			id, _ := m["id"].(string)
			name, _ := m["name"].(string)
			input := m["input"]
			thoughtSig, _ := m["thought_signature"].(string)
			pendingAssistant = append(pendingAssistant, llm.ContentBlock{
				Type:             "tool_use",
				ID:               id,
				Name:             name,
				Input:            input,
				ThoughtSignature: thoughtSig,
			})

		case "tool_result":
			flushAssistant()
			m := toStringMap(evt.Content)
			toolUseID, _ := m["tool_use_id"].(string)
			output, _ := m["output"].(string)
			isError, _ := m["is_error"].(bool)
			pendingToolResults = append(pendingToolResults, llm.ContentBlock{
				Type:      "tool_result",
				ToolUseID: toolUseID,
				Content:   output,
				IsError:   isError,
			})
		}
	}

	flushAssistant()
	flushToolResults()
	return fixDanglingToolUse(messages)
}

func extractUserMessageText(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case map[string]interface{}:
		var msg UserMessageContent
		data, _ := json.Marshal(v)
		if err := json.Unmarshal(data, &msg); err == nil {
			return buildUserPrompt(msg)
		}
	default:
		data, _ := json.Marshal(v)
		var msg UserMessageContent
		if err := json.Unmarshal(data, &msg); err == nil {
			return buildUserPrompt(msg)
		}
	}
	return ""
}

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

// fixDanglingToolUse inserts synthetic tool_results when an assistant message
// with tool_use is followed by a user message without tool_results.
// This happens when a page refresh interrupts a running agent.
func fixDanglingToolUse(messages []llm.ClaudeMessage) []llm.ClaudeMessage {
	var fixed []llm.ClaudeMessage
	for i, msg := range messages {
		fixed = append(fixed, msg)

		if msg.Role != "assistant" {
			continue
		}
		// Collect tool_use IDs from this assistant message
		var toolIDs []string
		for _, b := range msg.Content {
			if b.Type == "tool_use" {
				toolIDs = append(toolIDs, b.ID)
			}
		}
		if len(toolIDs) == 0 {
			continue
		}
		// Check if next message is user with tool_results
		hasResults := false
		if i+1 < len(messages) && messages[i+1].Role == "user" {
			for _, b := range messages[i+1].Content {
				if b.Type == "tool_result" {
					hasResults = true
					break
				}
			}
		}
		if hasResults {
			continue
		}
		// Inject synthetic tool_results
		var results []llm.ContentBlock
		for _, id := range toolIDs {
			results = append(results, llm.ContentBlock{
				Type:      "tool_result",
				ToolUseID: id,
				Content:   "Tool execution was interrupted.",
				IsError:   true,
			})
		}
		fixed = append(fixed, llm.ClaudeMessage{
			Role:    "user",
			Content: results,
		})
	}
	return fixed
}

func toInputMap(v interface{}) map[string]interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		return val
	default:
		data, _ := json.Marshal(v)
		var m map[string]interface{}
		json.Unmarshal(data, &m)
		return m
	}
}

func toStringMap(v interface{}) map[string]interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		return val
	default:
		data, _ := json.Marshal(v)
		var m map[string]interface{}
		json.Unmarshal(data, &m)
		return m
	}
}

// prefetchSessionImages downloads all image attachments in session events
// that are not yet cached into imgCache. Errors are logged but do not abort the agent.
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
