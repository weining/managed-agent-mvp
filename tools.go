package main

import (
	"encoding/json"
	"fmt"

	"managed-agent/llm"
)

// ToolDefinitions returns the tool schemas for Claude.
func ToolDefinitions(hasSkills bool, hasMemory bool) []llm.ClaudeTool {
	tools := []llm.ClaudeTool{
		{
			Name:        "execute_command",
			Description: "在 Linux 沙箱中执行 shell 命令。适合运行代码、安装依赖、短时操作。注意：超过 30 秒的长时运行任务（如启动服务器）请改用 shell_session。",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "要执行的 shell 命令"}
				},
				"required": ["command"]
			}`),
		},
		{
			Name:        "write_file",
			Description: "在沙箱中创建或覆盖文件。",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path":    {"type": "string", "description": "文件绝对路径"},
					"content": {"type": "string", "description": "文件内容"}
				},
				"required": ["path", "content"]
			}`),
		},
		{
			Name:        "read_file",
			Description: "读取沙箱中的文件内容。",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "文件绝对路径"}
				},
				"required": ["path"]
			}`),
		},
		{
			Name:        "browse_web",
			Description: "使用沙箱内置浏览器访问网页，提取页面内容（Markdown 格式）。适用于搜索信息、查看文档。支持 JS 渲染页面。",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"url": {"type": "string", "description": "要访问的网页 URL"}
				},
				"required": ["url"]
			}`),
		},
		{
			Name:        "take_screenshot",
			Description: "对网页截图并保存到沙箱。如果提供 URL 则先导航到该页面再截图，否则截取当前浏览器页面。",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"url": {"type": "string", "description": "要截图的网页 URL（可选，不提供则截当前页面）"}
				}
			}`),
		},
		// #2: MCP 工具
		{
			Name:        "mcp_tool",
			Description: "发现和调用沙箱中的 MCP 服务器工具。必须按顺序操作：先 list_servers 发现可用服务器，再 list_tools 查看该服务器的工具列表，最后 call_tool 调用具体工具。禁止跳过发现步骤直接猜测服务器或工具名。",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"action":      {"type": "string", "enum": ["list_servers", "list_tools", "call_tool"], "description": "操作类型"},
					"server_name": {"type": "string", "description": "MCP 服务器名称（list_tools 和 call_tool 时必需）"},
					"tool_name":   {"type": "string", "description": "工具名称（call_tool 时必需）"},
					"arguments":   {"type": "object", "description": "工具参数（call_tool 时使用）"}
				},
				"required": ["action"]
			}`),
		},
		// #3: 浏览器交互
		{
			Name:        "browser_action",
			Description: "与沙箱浏览器交互。navigate=导航到URL，click=点击元素，fill=填写输入框，type_text=逐字符输入，press_key=按键（Enter/Tab等），evaluate=执行JS，get_elements=获取可交互元素列表，scroll=滚动页面，get_console=查看控制台日志。",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"action":     {"type": "string", "enum": ["navigate", "click", "fill", "type_text", "press_key", "evaluate", "get_elements", "scroll", "get_console"], "description": "操作类型"},
					"url":        {"type": "string", "description": "目标 URL（navigate 时必需）"},
					"selector":   {"type": "string", "description": "CSS 选择器（click/fill 时使用）"},
					"index":      {"type": "integer", "description": "元素索引，从 get_elements 结果中获取（click/fill 时使用）"},
					"text":       {"type": "string", "description": "要填入的文本（fill/type_text 时必需）"},
					"key":        {"type": "string", "description": "要按的键名（press_key 时必需），如 Enter、Tab、Escape、ArrowDown"},
					"expression": {"type": "string", "description": "JavaScript 表达式（evaluate 时必需）"},
					"x":          {"type": "number", "description": "点击的 X 坐标（click 时可选）"},
					"y":          {"type": "number", "description": "点击的 Y 坐标（click 时可选）"},
					"direction":  {"type": "string", "enum": ["up", "down", "left", "right"], "description": "滚动方向（scroll 时使用）"},
					"amount":     {"type": "integer", "description": "滚动量（scroll 时使用，默认3）"}
				},
				"required": ["action"]
			}`),
		},
		// #4: 结构化代码执行
		{
			Name:        "execute_code",
			Description: "直接执行 Python 或 JavaScript 代码，返回结构化的 stdout/stderr/traceback。支持有状态会话（变量跨调用保持）。",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"language":   {"type": "string", "enum": ["python", "javascript"], "description": "编程语言"},
					"code":       {"type": "string", "description": "要执行的代码"},
					"session_id": {"type": "string", "description": "会话ID（可选，提供则启用有状态执行，变量在同一session中保持）"},
					"timeout":    {"type": "integer", "description": "超时时间（秒），默认30"}
				},
				"required": ["language", "code"]
			}`),
		},
		// #5: 文件搜索与目录浏览
		{
			Name:        "search_files",
			Description: "在沙箱中搜索或浏览文件。list=列出目录内容（推荐首次探索目录时使用），grep=按内容搜索，find=按文件名搜索。",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"action":           {"type": "string", "enum": ["list", "grep", "find"], "description": "操作：list=列目录，grep=内容搜索，find=文件名搜索"},
					"path":             {"type": "string", "description": "目录或搜索起点路径"},
					"pattern":          {"type": "string", "description": "搜索模式（grep=正则，find=glob如 *.py）；list 模式不需要"},
					"recursive":        {"type": "boolean", "description": "是否递归列出（list 时可选）"},
					"show_hidden":      {"type": "boolean", "description": "是否显示隐藏文件（list 时可选）"},
					"max_depth":        {"type": "integer", "description": "最大目录深度（list 时可选）"},
					"include":          {"type": "array", "items": {"type": "string"}, "description": "文件过滤（仅grep，如 [\"*.py\"]）"},
					"exclude":          {"type": "array", "items": {"type": "string"}, "description": "排除模式（仅grep）"},
					"case_insensitive": {"type": "boolean", "description": "忽略大小写（仅grep）"},
					"max_results":      {"type": "integer", "description": "最大结果数"}
				},
				"required": ["action", "path"]
			}`),
		},
		// #6: Shell 会话管理
		{
			Name:        "shell_session",
			Description: "管理长运行 Shell 会话。适合启动服务器、监听日志等需要持续运行的任务（与 execute_command 区别：execute_command 适合短时操作，shell_session 适合长时后台进程）。支持创建会话、异步执行命令、查看输出、写入stdin、终止进程。",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"action":     {"type": "string", "enum": ["create", "exec", "view", "write", "kill", "list"], "description": "操作类型"},
					"session_id": {"type": "string", "description": "会话ID（exec/view/write/kill 时使用）"},
					"command":    {"type": "string", "description": "要执行的命令（exec 时必需）"},
					"input":      {"type": "string", "description": "写入stdin的内容（write 时必需）"},
					"async":      {"type": "boolean", "description": "异步执行（exec 时可选，默认false）"},
					"timeout":    {"type": "number", "description": "超时时间秒（exec 时可选）"}
				},
				"required": ["action"]
			}`),
		},
		// #6: 屏幕录制
		{
			Name:        "display_record",
			Description: "录制沙箱桌面屏幕（包括浏览器、终端等所有内容）。支持开始/停止/查询状态。",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"action": {"type": "string", "enum": ["start", "stop", "status"], "description": "操作：start=开始录制，stop=停止录制，status=查询状态"}
				},
				"required": ["action"]
			}`),
		},
	}

	// File download - always available
	tools = append(tools, llm.ClaudeTool{
		Name:        "download_file",
		Description: "生成沙箱文件的下载链接，用户可通过浏览器下载。适用于生成的文档、图片、压缩包等需要交付给用户的文件。",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "沙箱中的文件绝对路径"}
			},
			"required": ["path"]
		}`),
	})

	// File edit - precise file editing via StrReplaceEditor
	tools = append(tools, llm.ClaudeTool{
		Name:        "file_edit",
		Description: "精准编辑沙箱中的文件，推荐用于代码修改（避免 write_file 覆盖整个文件）。view=查看文件内容（含行号），str_replace=精准替换字符串，create=新建文件，insert=在指定行后插入内容，undo_edit=撤销上一次编辑。",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command":     {"type": "string", "enum": ["view", "str_replace", "create", "insert", "undo_edit"], "description": "操作类型"},
				"path":        {"type": "string", "description": "文件绝对路径"},
				"old_str":     {"type": "string", "description": "要替换的原始字符串（str_replace 时必需，必须在文件中唯一匹配）"},
				"new_str":     {"type": "string", "description": "替换后的新字符串（str_replace/insert 时使用）"},
				"file_text":   {"type": "string", "description": "新文件内容（create 时必需）"},
				"insert_line": {"type": "integer", "description": "在该行号之后插入内容（insert 时必需，1-indexed）"},
				"view_range":  {"type": "array", "items": {"type": "integer"}, "description": "查看行范围 [start, end]（view 时可选）"}
			},
			"required": ["command", "path"]
		}`),
	})

	if hasSkills {
		tools = append(tools, llm.ClaudeTool{
			Name:        "skill",
			Description: "管理 Skills。activate=激活一个 skill 获得专门工作流指导，deactivate=停用一个 skill，list=查看所有可用 skill。",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"action": {"type": "string", "enum": ["activate", "deactivate", "list"], "description": "操作类型"},
					"name":   {"type": "string", "description": "skill 名称（activate/deactivate 时必需）"}
				},
				"required": ["action"]
			}`),
		})
	}

	if hasMemory {
		tools = append(tools, llm.ClaudeTool{
			Name:        "memory",
			Description: "管理跨会话持久记忆。save=保存记忆，recall=搜索回忆，delete=删除记忆，list=列出记忆。",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"action":  {"type": "string", "enum": ["save", "recall", "delete", "list"], "description": "操作类型"},
					"key":     {"type": "string", "description": "记忆键名（save/delete 时必需）"},
					"content": {"type": "string", "description": "记忆内容（save 时必需）"},
					"tags":    {"type": "array", "items": {"type": "string"}, "description": "分类标签"},
					"query":   {"type": "string", "description": "搜索关键词（recall 时使用）"},
					"limit":   {"type": "integer", "description": "返回数量上限，默认10"}
				},
				"required": ["action"]
			}`),
		})
	}

	return tools
}

// ExecuteTool routes a tool call to the sandbox via the SDK client.
func ExecuteTool(sbx *SDKSandboxClient, name string, input map[string]any) (string, bool) {
	switch name {
	case "execute_command":
		cmd, _ := input["command"].(string)
		stdout, exitCode, err := sbx.ExecCommand(cmd)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		if exitCode != 0 {
			return fmt.Sprintf("Exit code: %d\n%s", exitCode, stdout), false
		}
		return stdout, false

	case "write_file":
		path, _ := input["path"].(string)
		content, _ := input["content"].(string)
		if err := sbx.WriteFile(path, content); err != nil {
			return "Error: " + err.Error(), true
		}
		return "File written successfully: " + path, false

	case "read_file":
		path, _ := input["path"].(string)
		content, err := sbx.ReadFile(path)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return content, false

	case "browse_web":
		url, _ := input["url"].(string)
		result, err := sbx.BrowseWebSDK(url)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false

	case "take_screenshot":
		url, _ := input["url"].(string)
		result, err := sbx.TakeScreenshotSDK(url)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false

	case "mcp_tool":
		return executeMcpTool(sbx, input)

	case "browser_action":
		return executeBrowserAction(sbx, input)

	case "execute_code":
		return executeCodeTool(sbx, input)

	case "search_files":
		return executeSearchFiles(sbx, input)

	case "shell_session":
		return executeShellSession(sbx, input)

	case "display_record":
		return executeDisplayRecord(sbx, input)

	case "download_file":
		return executeDownloadFile(input)

	case "file_edit":
		return executeFileEdit(sbx, input)

	default:
		return "Unknown tool: " + name, true
	}
}

func executeMcpTool(sbx *SDKSandboxClient, input map[string]any) (string, bool) {
	action, _ := input["action"].(string)
	switch action {
	case "list_servers":
		result, err := sbx.McpListServers()
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	case "list_tools":
		server, _ := input["server_name"].(string)
		if server == "" {
			return "Error: server_name is required for list_tools", true
		}
		result, err := sbx.McpListTools(server)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	case "call_tool":
		server, _ := input["server_name"].(string)
		tool, _ := input["tool_name"].(string)
		if server == "" || tool == "" {
			return "Error: server_name and tool_name are required for call_tool", true
		}
		args, _ := input["arguments"].(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		result, err := sbx.McpCallTool(server, tool, args)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	default:
		return "Error: invalid mcp_tool action: " + action, true
	}
}

func executeBrowserAction(sbx *SDKSandboxClient, input map[string]any) (string, bool) {
	action, _ := input["action"].(string)
	switch action {
	case "navigate":
		url, _ := input["url"].(string)
		if url == "" {
			return "Error: url is required for navigate", true
		}
		result, err := sbx.BrowserNavigate(url)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	case "click":
		selector, _ := input["selector"].(string)
		var idx *int
		if v, ok := input["index"].(float64); ok {
			i := int(v)
			idx = &i
		}
		var x, y *float64
		if v, ok := input["x"].(float64); ok {
			x = &v
		}
		if v, ok := input["y"].(float64); ok {
			y = &v
		}
		result, err := sbx.BrowserClick(selector, idx, x, y)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	case "fill":
		selector, _ := input["selector"].(string)
		text, _ := input["text"].(string)
		var idx *int
		if v, ok := input["index"].(float64); ok {
			i := int(v)
			idx = &i
		}
		result, err := sbx.BrowserFill(selector, idx, text)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	case "type_text":
		text, _ := input["text"].(string)
		if text == "" {
			return "Error: text is required for type_text", true
		}
		result, err := sbx.BrowserTypeText(text, nil)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	case "press_key":
		key, _ := input["key"].(string)
		if key == "" {
			return "Error: key is required for press_key", true
		}
		result, err := sbx.BrowserPressKey(key)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	case "evaluate":
		expr, _ := input["expression"].(string)
		if expr == "" {
			return "Error: expression is required for evaluate", true
		}
		result, err := sbx.BrowserEvaluate(expr)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	case "get_elements":
		result, err := sbx.BrowserGetElements()
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	case "scroll":
		direction, _ := input["direction"].(string)
		var amount *int
		if v, ok := input["amount"].(float64); ok {
			i := int(v)
			amount = &i
		}
		result, err := sbx.BrowserScroll(direction, amount)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	case "get_console":
		result, err := sbx.BrowserGetConsole()
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	default:
		return "Error: invalid browser_action: " + action, true
	}
}

func executeCodeTool(sbx *SDKSandboxClient, input map[string]any) (string, bool) {
	language, _ := input["language"].(string)
	code, _ := input["code"].(string)
	sessionID, _ := input["session_id"].(string)
	timeout := 30
	if v, ok := input["timeout"].(float64); ok {
		timeout = int(v)
	}

	result, err := sbx.ExecuteCode(language, code, sessionID, timeout)
	if err != nil {
		return "Error: " + err.Error(), true
	}
	return result, false
}

func executeSearchFiles(sbx *SDKSandboxClient, input map[string]any) (string, bool) {
	action, _ := input["action"].(string)
	path, _ := input["path"].(string)
	pattern, _ := input["pattern"].(string)

	switch action {
	case "list":
		recursive, _ := input["recursive"].(bool)
		showHidden, _ := input["show_hidden"].(bool)
		var maxDepth *int
		if v, ok := input["max_depth"].(float64); ok {
			n := int(v)
			maxDepth = &n
		}
		result, err := sbx.FileListPath(path, recursive, showHidden, maxDepth)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	case "grep":
		var include, exclude []string
		if v, ok := input["include"].([]any); ok {
			for _, item := range v {
				if s, ok := item.(string); ok {
					include = append(include, s)
				}
			}
		}
		if v, ok := input["exclude"].([]any); ok {
			for _, item := range v {
				if s, ok := item.(string); ok {
					exclude = append(exclude, s)
				}
			}
		}
		caseInsensitive, _ := input["case_insensitive"].(bool)
		maxResults := 0
		if v, ok := input["max_results"].(float64); ok {
			maxResults = int(v)
		}
		result, err := sbx.FileGrep(path, pattern, include, exclude, caseInsensitive, maxResults)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	case "find":
		result, err := sbx.FileFind(path, pattern)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	default:
		return "Error: invalid search_files action: " + action, true
	}
}

func executeFileEdit(sbx *SDKSandboxClient, input map[string]any) (string, bool) {
	command, _ := input["command"].(string)
	path, _ := input["path"].(string)
	if command == "" || path == "" {
		return "Error: command and path are required for file_edit", true
	}
	result, err := sbx.FileStrReplaceEditor(command, path, input)
	if err != nil {
		return "Error: " + err.Error(), true
	}
	return result, false
}

func executeShellSession(sbx *SDKSandboxClient, input map[string]any) (string, bool) {
	action, _ := input["action"].(string)
	switch action {
	case "create":
		result, err := sbx.ShellCreateSession()
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	case "exec":
		sessionID, _ := input["session_id"].(string)
		command, _ := input["command"].(string)
		if command == "" {
			return "Error: command is required for exec", true
		}
		asyncMode, _ := input["async"].(bool)
		timeout := 0.0
		if v, ok := input["timeout"].(float64); ok {
			timeout = v
		}
		result, err := sbx.ShellExecInSession(sessionID, command, asyncMode, timeout)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	case "view":
		sessionID, _ := input["session_id"].(string)
		if sessionID == "" {
			return "Error: session_id is required for view", true
		}
		result, err := sbx.ShellViewSession(sessionID)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	case "write":
		sessionID, _ := input["session_id"].(string)
		inputStr, _ := input["input"].(string)
		if sessionID == "" || inputStr == "" {
			return "Error: session_id and input are required for write", true
		}
		result, err := sbx.ShellWriteToProcess(sessionID, inputStr)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	case "kill":
		sessionID, _ := input["session_id"].(string)
		if sessionID == "" {
			return "Error: session_id is required for kill", true
		}
		result, err := sbx.ShellKillProcess(sessionID)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	case "list":
		result, err := sbx.ShellListSessions()
		if err != nil {
			return "Error: " + err.Error(), true
		}
		return result, false
	default:
		return "Error: invalid shell_session action: " + action, true
	}
}

func executeDisplayRecord(sbx *SDKSandboxClient, input map[string]any) (string, bool) {
	action, _ := input["action"].(string)
	result, err := sbx.DisplayRecord(action)
	if err != nil {
		return "Error: " + err.Error(), true
	}
	return result, false
}

// executeDownloadFile generates a download URL for a sandbox file.
// Returns a JSON payload that the frontend can parse to render a download button.
func executeDownloadFile(input map[string]any) (string, bool) {
	path, _ := input["path"].(string)
	if path == "" {
		return "Error: path is required", true
	}
	result, _ := json.Marshal(map[string]string{
		"type": "download",
		"path": path,
	})
	return string(result), false
}

// executeMemoryTool handles the "memory" tool calls.
func executeMemoryTool(store MemoryStore, sessionID string, input map[string]any) (string, bool) {
	action, _ := input["action"].(string)
	limit := 10
	if v, ok := input["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}

	switch action {
	case "save":
		key, _ := input["key"].(string)
		content, _ := input["content"].(string)
		if key == "" || content == "" {
			return "Error: key and content are required for save", true
		}
		var tags []string
		if v, ok := input["tags"].([]any); ok {
			for _, item := range v {
				if s, ok := item.(string); ok {
					tags = append(tags, s)
				}
			}
		}
		entry := MemoryEntry{
			Key:     key,
			Content: content,
			Tags:    tags,
			Source:  sessionID,
		}
		if err := store.Save(entry); err != nil {
			return "Error: " + err.Error(), true
		}
		return fmt.Sprintf("Memory saved: %s", key), false

	case "recall":
		query, _ := input["query"].(string)
		var tags []string
		if v, ok := input["tags"].([]any); ok {
			for _, item := range v {
				if s, ok := item.(string); ok {
					tags = append(tags, s)
				}
			}
		}
		results, err := store.Search(query, tags, limit)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		if len(results) == 0 {
			return "No memories found.", false
		}
		data, _ := json.MarshalIndent(results, "", "  ")
		return string(data), false

	case "delete":
		key, _ := input["key"].(string)
		if key == "" {
			return "Error: key is required for delete", true
		}
		if err := store.Delete(key); err != nil {
			return "Error: " + err.Error(), true
		}
		return fmt.Sprintf("Memory deleted: %s", key), false

	case "list":
		var tags []string
		if v, ok := input["tags"].([]any); ok {
			for _, item := range v {
				if s, ok := item.(string); ok {
					tags = append(tags, s)
				}
			}
		}
		results, err := store.List(tags, limit)
		if err != nil {
			return "Error: " + err.Error(), true
		}
		if len(results) == 0 {
			return "No memories stored.", false
		}
		data, _ := json.MarshalIndent(results, "", "  ")
		return string(data), false

	default:
		return "Error: invalid memory action: " + action, true
	}
}
