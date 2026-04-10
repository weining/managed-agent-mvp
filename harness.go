package main

import (
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"
)

const (
	maxLoopRounds    = 50
	baseSystemPrompt = `你是一个强大的 AI 助手，运行在一个功能丰富的 AIO (All-In-One) 沙箱环境中。

## 环境能力
- **系统**: Ubuntu 22.04, 32核 CPU, 125GB 内存, 2TB 磁盘
- **编程语言**: Python 3.10 (含 numpy/pandas/matplotlib/opencv/requests/fastapi/httpx/beautifulsoup4), Node.js 22, GCC 11
- **浏览器**: Chromium 135 (完整交互能力：导航、点击、填写、截图、执行JS)
- **工具**: git, jq, ImageMagick (convert), curl, wget
- **联网**: 完整外网访问，可以 pip install / npm install 安装额外包
- **显示**: Xvnc 虚拟桌面 1280x1024, 支持 GUI 应用和屏幕录制

## 工具使用策略

### 基础工具
- **execute_command**: 执行任意 shell 命令。适合运行代码、安装包、系统操作
- **write_file**: 写入文件。写代码文件后记得用 execute_command 运行验证
- **read_file**: 读取文件内容

### 浏览器工具
- **browse_web**: 访问网页并提取 Markdown 格式内容。适合搜索信息、查看文档
  - 搜索: browse_web("https://www.baidu.com/s?wd=关键词")
  - 注意: 沙箱无法访问 Google，请使用百度搜索
- **take_screenshot**: 对网页截图。适合验证前端效果、记录页面状态
- **browser_action**: 与浏览器交互（点击、填写表单、执行JS、获取元素列表等）
  - 先用 get_elements 获取页面可交互元素列表
  - 再用 click/fill 操作具体元素（通过 selector 或 index）
  - 用 evaluate 执行任意 JavaScript 代码
  - 用 scroll 滚动页面
  - 用 get_console 查看浏览器控制台日志

### 代码执行
- **execute_code**: 直接执行 Python/JavaScript，返回结构化的 stdout/stderr/traceback
  - 支持有状态会话：提供 session_id 可保持变量跨调用
  - 适合交互式数据分析、逐步调试

### 文件搜索
- **search_files**: 在沙箱中搜索文件
  - grep: 搜索文件内容（正则表达式），返回匹配的文件:行号:内容
  - find: 按文件名搜索（glob模式如 *.py）

### MCP 工具
- **mcp_tool**: 发现和调用沙箱中配置的 MCP 服务器工具
  - 先 list_servers 查看可用服务器
  - 再 list_tools 查看某服务器的工具
  - 最后 call_tool 调用具体工具

### Shell 会话管理
- **shell_session**: 管理长运行进程（如启动开发服务器）
  - create: 创建新会话
  - exec: 在会话中执行命令（支持异步）
  - view: 查看会话输出
  - write: 向进程写入 stdin
  - kill: 终止进程
  - list: 列出所有活跃会话

### 屏幕录制
- **display_record**: 录制沙箱桌面
  - start: 开始录制, stop: 停止录制, status: 查询状态

### 文件下载
- **download_file**: 生成沙箱文件的下载链接。当你在沙箱中生成了文件（如文档、图片、压缩包）需要交付给用户时使用此工具
  - 提供文件的绝对路径，会返回可供用户点击下载的链接

## 行为准则
- 直接动手解决问题，先写代码再运行验证
- 遇到错误时分析原因并修复，不要只是报告错误
- 用中文回复用户，代码注释可以用英文
- 保持回复简洁，避免冗长解释
- 需要查询信息时，主动使用 browse_web 搜索
- 前端开发时，可以启动 HTTP server 并用 take_screenshot 验证效果
- 需要与网页交互时（填表单、点击按钮），使用 browser_action`
)

// SSEWriter is the interface for streaming events to the client.
type SSEWriter interface {
	WriteEvent(event, data string)
	Flush()
}

// AgentDeps groups the dependencies injected into RunAgent.
type AgentDeps struct {
	Store   *SessionStore
	Sandbox *SDKSandboxClient
	Claude  *ClaudeClient
	Skills  *SkillRegistry
}

// buildSystemPrompt composes the full system prompt from the base, skill
// summary, and any active skill prompts.
func buildSystemPrompt(skills *SkillRegistry, activeSkills []string, skillArgs map[string]string) string {
	var sb strings.Builder
	sb.WriteString(baseSystemPrompt)
	sb.WriteString(skills.SkillSummary())
	sb.WriteString(skills.ActiveSkillsPrompt(activeSkills, skillArgs))
	return sb.String()
}

// RunAgent executes the agent loop for a user message in a session.
func RunAgent(deps *AgentDeps, sess *Session, userMsg string, sse SSEWriter) error {
	store := deps.Store

	// 1. Emit user message event
	store.EmitEvent(sess.ID, Event{
		Type:    "user_message",
		Content: userMsg,
	})

	hasSkills := len(deps.Skills.List()) > 0
	tools := ToolDefinitions(hasSkills)

	for round := 0; round < maxLoopRounds; round++ {
		// Reload session to get latest events (including skill changes)
		sess, _ = store.Get(sess.ID)

		systemPrompt := buildSystemPrompt(deps.Skills, sess.ActiveSkills, sess.SkillArgs)
		messages := buildMessages(sess.Events)

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
		var toolResults []ContentBlock

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

				store.EmitEvent(sess.ID, Event{
					Type: "tool_use",
					Content: map[string]interface{}{
						"id":    block.ID,
						"name":  block.Name,
						"input": inputMap,
					},
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

				toolResults = append(toolResults, ContentBlock{
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
	return fmt.Errorf("agent loop exceeded %d rounds", maxLoopRounds)
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
func buildMessages(events []Event) []ClaudeMessage {
	var messages []ClaudeMessage
	var pendingAssistant []ContentBlock
	var pendingToolResults []ContentBlock

	flushAssistant := func() {
		if len(pendingAssistant) > 0 {
			messages = append(messages, ClaudeMessage{
				Role:    "assistant",
				Content: pendingAssistant,
			})
			pendingAssistant = nil
		}
	}

	flushToolResults := func() {
		if len(pendingToolResults) > 0 {
			messages = append(messages, ClaudeMessage{
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
			text, _ := evt.Content.(string)
			messages = append(messages, ClaudeMessage{
				Role: "user",
				Content: []ContentBlock{
					{Type: "text", Text: text},
				},
			})

		case "assistant_message":
			flushToolResults()
			text, _ := evt.Content.(string)
			pendingAssistant = append(pendingAssistant, ContentBlock{
				Type: "text",
				Text: text,
			})

		case "tool_use":
			flushToolResults()
			m := toStringMap(evt.Content)
			id, _ := m["id"].(string)
			name, _ := m["name"].(string)
			input := m["input"]
			pendingAssistant = append(pendingAssistant, ContentBlock{
				Type:  "tool_use",
				ID:    id,
				Name:  name,
				Input: input,
			})

		case "tool_result":
			flushAssistant()
			m := toStringMap(evt.Content)
			toolUseID, _ := m["tool_use_id"].(string)
			output, _ := m["output"].(string)
			isError, _ := m["is_error"].(bool)
			pendingToolResults = append(pendingToolResults, ContentBlock{
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

// fixDanglingToolUse inserts synthetic tool_results when an assistant message
// with tool_use is followed by a user message without tool_results.
// This happens when a page refresh interrupts a running agent.
func fixDanglingToolUse(messages []ClaudeMessage) []ClaudeMessage {
	var fixed []ClaudeMessage
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
		var results []ContentBlock
		for _, id := range toolIDs {
			results = append(results, ContentBlock{
				Type:      "tool_result",
				ToolUseID: id,
				Content:   "Tool execution was interrupted.",
				IsError:   true,
			})
		}
		fixed = append(fixed, ClaudeMessage{
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
