package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	api "github.com/agent-infra/sandbox-sdk-go"
	"github.com/agent-infra/sandbox-sdk-go/client"
	"github.com/agent-infra/sandbox-sdk-go/option"
)

// namedReader wraps an io.Reader and implements the Named interface required by
// the SDK's multipart writer to set the filename on the form field.
type namedReader struct {
	io.Reader
	name string
}

func (n *namedReader) Name() string { return n.name }

// SDKSandboxClient wraps the sandbox-sdk-go client.
type SDKSandboxClient struct {
	client    *client.Client
	BaseURL   string
	SandboxID string
}

func NewSDKSandboxClient(baseURL, sandboxID string) *SDKSandboxClient {
	c := client.NewClient(option.WithBaseURL(baseURL))
	return &SDKSandboxClient{
		client:    c,
		BaseURL:   baseURL,
		SandboxID: sandboxID,
	}
}

// Init verifies connectivity.
func (s *SDKSandboxClient) Init() error {
	ctx := context.Background()
	_, err := s.client.File.ListPath(ctx, &api.FileListRequest{Path: "/"})
	if err != nil {
		return fmt.Errorf("failed to connect to sandbox SDK: %w", err)
	}
	return nil
}

// --- Basic: exec / read / write ---

func (s *SDKSandboxClient) ExecCommand(command string) (stdout string, exitCode int, err error) {
	ctx := context.Background()
	resp, err := s.client.Shell.ExecCommand(ctx, &api.ShellExecRequest{
		Command: command,
		Timeout: api.Float64(120),
	})
	if err != nil {
		return "", -1, fmt.Errorf("failed to exec command: %w", err)
	}
	if resp.Data == nil {
		return "", -1, fmt.Errorf("exec returned nil data")
	}
	var out string
	if resp.Data.Output != nil {
		out = *resp.Data.Output
	}
	var code int
	if resp.Data.ExitCode != nil {
		code = *resp.Data.ExitCode
	}
	return out, code, nil
}

func (s *SDKSandboxClient) WriteFile(path, content string) error {
	ctx := context.Background()
	_, err := s.client.File.WriteFile(ctx, &api.FileWriteRequest{
		File:    path,
		Content: content,
	})
	if err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}
	return nil
}

func (s *SDKSandboxClient) ReadFile(path string) (string, error) {
	ctx := context.Background()
	resp, err := s.client.File.ReadFile(ctx, &api.FileReadRequest{
		File: path,
	})
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	if resp.Data == nil {
		return "", fmt.Errorf("read file returned nil data")
	}
	return resp.Data.Content, nil
}

// --- #1: browse_web via BrowserPage SDK ---

func (s *SDKSandboxClient) BrowseWebSDK(targetURL string) (string, error) {
	ctx := context.Background()

	// Navigate to the target URL
	_, err := s.client.BrowserPage.Navigate(ctx, &api.NavigateRequest{Url: targetURL})
	if err != nil {
		return "", fmt.Errorf("browse_web navigate failed: %w", err)
	}

	// Get page content as Markdown (uses Readability + Turndown for clean extraction)
	mdResp, err := s.client.BrowserPage.GetMarkdown(ctx)
	if err != nil {
		// Fallback to plain text if Markdown fails
		textResp, textErr := s.client.BrowserPage.GetText(ctx)
		if textErr != nil {
			return "", fmt.Errorf("browse_web content extraction failed: %w", err)
		}
		if textResp.Data != nil && *textResp.Data != "" {
			return fmt.Sprintf("URL: %s\n\n%s", targetURL, *textResp.Data), nil
		}
		return "", fmt.Errorf("browse_web: no content retrieved")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "URL: %s\n\n", targetURL)
	if mdResp.Data != nil {
		if title, ok := mdResp.Data["title"].(string); ok && title != "" {
			fmt.Fprintf(&sb, "Title: %s\n\n", title)
		}
		if content, ok := mdResp.Data["content"].(string); ok {
			sb.WriteString(content)
		} else {
			data, _ := json.MarshalIndent(mdResp.Data, "", "  ")
			sb.Write(data)
		}
	}
	return sb.String(), nil
}

// --- #1: take_screenshot via BrowserPage SDK ---

func (s *SDKSandboxClient) TakeScreenshotSDK(targetURL string) (string, error) {
	ctx := context.Background()

	// Navigate if URL provided
	if targetURL != "" {
		if _, err := s.client.BrowserPage.Navigate(ctx, &api.NavigateRequest{Url: targetURL}); err != nil {
			return "", fmt.Errorf("screenshot navigate failed: %w", err)
		}
	}

	reader, err := s.client.Browser.Screenshot(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to take screenshot: %w", err)
	}
	imgData, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("failed to read screenshot data: %w", err)
	}

	savePath := fmt.Sprintf("/tmp/screenshot_%d.png", time.Now().Unix())
	if err := s.writeFileBytes(savePath, imgData); err != nil {
		return "", fmt.Errorf("failed to save screenshot: %w", err)
	}
	return fmt.Sprintf("Screenshot saved: %s (%d bytes)", savePath, len(imgData)), nil
}

// --- #2: MCP tools ---

func (s *SDKSandboxClient) McpListServers() (string, error) {
	ctx := context.Background()
	resp, err := s.client.Mcp.ListMcpServers(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list MCP servers: %w", err)
	}
	if resp.Data == nil || len(resp.Data) == 0 {
		return "No MCP servers configured.", nil
	}
	data, _ := json.MarshalIndent(resp.Data, "", "  ")
	return string(data), nil
}

func (s *SDKSandboxClient) McpListTools(serverName string) (string, error) {
	ctx := context.Background()
	resp, err := s.client.Mcp.ListMcpTools(ctx, serverName)
	if err != nil {
		return "", fmt.Errorf("failed to list MCP tools for %s: %w", serverName, err)
	}
	if resp.Data == nil || len(resp.Data.Tools) == 0 {
		return fmt.Sprintf("No tools found for MCP server: %s", serverName), nil
	}
	var sb strings.Builder
	for _, t := range resp.Data.Tools {
		desc := ""
		if t.Description != nil {
			desc = *t.Description
		}
		fmt.Fprintf(&sb, "- %s: %s\n", t.Name, desc)
	}
	return sb.String(), nil
}

func (s *SDKSandboxClient) McpCallTool(serverName, toolName string, args map[string]interface{}) (string, error) {
	ctx := context.Background()
	resp, err := s.client.Mcp.ExecuteMcpTool(ctx, serverName, toolName, args)
	if err != nil {
		return "", fmt.Errorf("failed to call MCP tool %s/%s: %w", serverName, toolName, err)
	}
	if resp.Data == nil {
		return "MCP tool returned no result.", nil
	}
	if resp.Data.IsError != nil && *resp.Data.IsError {
		for _, item := range resp.Data.Content {
			if item.Text != nil {
				return item.Text.Text, fmt.Errorf("MCP tool error: %s", item.Text.Text)
			}
		}
		return "MCP tool returned an error.", fmt.Errorf("MCP tool returned an error")
	}
	var sb strings.Builder
	for _, item := range resp.Data.Content {
		if item.Text != nil {
			sb.WriteString(item.Text.Text)
			sb.WriteString("\n")
		}
	}
	if sb.Len() > 0 {
		return strings.TrimSpace(sb.String()), nil
	}
	if resp.Data.StructuredContent != nil {
		data, _ := json.MarshalIndent(resp.Data.StructuredContent, "", "  ")
		return string(data), nil
	}
	data, _ := json.MarshalIndent(resp.Data, "", "  ")
	return string(data), nil
}

// --- #3: Browser interaction via BrowserPage SDK ---

func (s *SDKSandboxClient) BrowserClick(selector string, index *int, x, y *float64) (string, error) {
	ctx := context.Background()
	req := &api.ClickRequest{}
	if selector != "" {
		req.Selector = &selector
	}
	if index != nil {
		req.Index = index
	}
	if x != nil {
		req.X = x
	}
	if y != nil {
		req.Y = y
	}
	if _, err := s.client.BrowserPage.Click(ctx, req); err != nil {
		return "", fmt.Errorf("click failed: %w", err)
	}
	return "Clicked successfully.", nil
}

func (s *SDKSandboxClient) BrowserFill(selector string, index *int, text string) (string, error) {
	ctx := context.Background()
	req := &api.FillRequest{Text: text}
	if selector != "" {
		req.Selector = &selector
	}
	if index != nil {
		req.Index = index
	}
	if _, err := s.client.BrowserPage.Fill(ctx, req); err != nil {
		return "", fmt.Errorf("fill failed: %w", err)
	}
	return "Filled successfully.", nil
}

func (s *SDKSandboxClient) BrowserEvaluate(expression string) (string, error) {
	ctx := context.Background()
	resp, err := s.client.BrowserPage.Evaluate(ctx, &api.EvaluateRequest{Expression: expression})
	if err != nil {
		return "", fmt.Errorf("evaluate failed: %w", err)
	}
	if resp == nil || resp.Data == nil {
		return "undefined", nil
	}
	switch v := resp.Data.(type) {
	case string:
		return v, nil
	default:
		data, _ := json.Marshal(v)
		return string(data), nil
	}
}

func (s *SDKSandboxClient) BrowserGetElements() (string, error) {
	ctx := context.Background()
	resp, err := s.client.BrowserPage.GetElements(ctx)
	if err != nil {
		return "", fmt.Errorf("get_elements failed: %w", err)
	}
	if resp == nil || len(resp.Data) == 0 {
		return "No interactive elements found.", nil
	}
	data, _ := json.MarshalIndent(resp.Data, "", "  ")
	return string(data), nil
}

func (s *SDKSandboxClient) BrowserScroll(direction string, amount *int) (string, error) {
	ctx := context.Background()
	req := &api.ScrollRequest{}
	if direction != "" {
		req.Direction = &direction
	}
	if amount != nil {
		req.Amount = amount
	}
	if _, err := s.client.BrowserPage.Scroll(ctx, req); err != nil {
		return "", fmt.Errorf("scroll failed: %w", err)
	}
	return fmt.Sprintf("Scrolled %s.", direction), nil
}

func (s *SDKSandboxClient) BrowserGetConsole() (string, error) {
	ctx := context.Background()
	resp, err := s.client.BrowserPage.GetConsole(ctx, &api.BrowserPageGetConsoleRequest{})
	if err != nil {
		return "", fmt.Errorf("get_console failed: %w", err)
	}
	if resp == nil || len(resp.Data) == 0 {
		return "No console logs.", nil
	}
	data, _ := json.MarshalIndent(resp.Data, "", "  ")
	return string(data), nil
}

// BrowserTypeText types text into the currently focused element.
func (s *SDKSandboxClient) BrowserTypeText(text string, delay *float64) (string, error) {
	ctx := context.Background()
	req := &api.TypeTextRequest{Text: text}
	if delay != nil {
		req.Delay = delay
	}
	if _, err := s.client.BrowserPage.TypeText(ctx, req); err != nil {
		return "", fmt.Errorf("type_text failed: %w", err)
	}
	return "Text typed successfully.", nil
}

// BrowserPressKey presses a single keyboard key (e.g. "Enter", "Tab", "Escape").
func (s *SDKSandboxClient) BrowserPressKey(key string) (string, error) {
	ctx := context.Background()
	if _, err := s.client.BrowserPage.PressKey(ctx, &api.KeyRequest{Key: key}); err != nil {
		return "", fmt.Errorf("press_key failed: %w", err)
	}
	return fmt.Sprintf("Key pressed: %s", key), nil
}

// BrowserNavigate navigates the browser to the specified URL.
func (s *SDKSandboxClient) BrowserNavigate(targetURL string) (string, error) {
	ctx := context.Background()
	if _, err := s.client.BrowserPage.Navigate(ctx, &api.NavigateRequest{Url: targetURL}); err != nil {
		return "", fmt.Errorf("navigate failed: %w", err)
	}
	return fmt.Sprintf("Navigated to: %s", targetURL), nil
}

// --- #4: Code execution ---

func (s *SDKSandboxClient) ExecuteCode(language, code string, sessionID string, timeout int) (string, error) {
	ctx := context.Background()

	var lang api.Language
	switch language {
	case "python":
		lang = api.LanguagePython
	case "javascript":
		lang = api.LanguageJavascript
	default:
		return "", fmt.Errorf("unsupported language: %s (use 'python' or 'javascript')", language)
	}

	req := &api.CodeExecuteRequest{
		Language: lang,
		Code:     code,
	}
	if timeout > 0 {
		req.Timeout = &timeout
	}
	if sessionID != "" {
		req.Stateful = api.Bool(true)
		req.SessionId = &sessionID
	}

	resp, err := s.client.Code.ExecuteCode(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to execute code: %w", err)
	}
	if resp.Data == nil {
		return "", fmt.Errorf("code execution returned nil data")
	}
	d := resp.Data

	var sb strings.Builder
	fmt.Fprintf(&sb, "Status: %s\n", d.Status)
	if d.SessionId != nil {
		fmt.Fprintf(&sb, "SessionID: %s\n", *d.SessionId)
	}
	if d.Stdout != nil && *d.Stdout != "" {
		fmt.Fprintf(&sb, "\n--- stdout ---\n%s\n", *d.Stdout)
	}
	if d.Stderr != nil && *d.Stderr != "" {
		fmt.Fprintf(&sb, "\n--- stderr ---\n%s\n", *d.Stderr)
	}
	if len(d.Traceback) > 0 {
		fmt.Fprintf(&sb, "\n--- traceback ---\n%s\n", strings.Join(d.Traceback, "\n"))
	}
	if d.ExitCode != nil {
		fmt.Fprintf(&sb, "\nExit code: %d\n", *d.ExitCode)
	}
	return strings.TrimSpace(sb.String()), nil
}

// --- #5: File search ---

func (s *SDKSandboxClient) FileGrep(path, pattern string, include, exclude []string, caseInsensitive bool, maxResults int) (string, error) {
	ctx := context.Background()
	req := &api.FileGrepRequest{
		Path:    path,
		Pattern: pattern,
	}
	if len(include) > 0 {
		req.Include = include
	}
	if len(exclude) > 0 {
		req.Exclude = exclude
	}
	if caseInsensitive {
		req.CaseInsensitive = api.Bool(true)
	}
	if maxResults > 0 {
		req.MaxResults = &maxResults
	}

	resp, err := s.client.File.GrepFiles(ctx, req)
	if err != nil {
		return "", fmt.Errorf("grep failed: %w", err)
	}
	if resp.Data == nil {
		return "No results.", nil
	}
	d := resp.Data
	var sb strings.Builder
	if d.MatchCount != nil {
		fmt.Fprintf(&sb, "Matches: %d", *d.MatchCount)
		if d.FilesMatched != nil {
			fmt.Fprintf(&sb, " in %d files", *d.FilesMatched)
		}
		if d.Truncated != nil && *d.Truncated {
			sb.WriteString(" (truncated)")
		}
		sb.WriteString("\n\n")
	}
	for _, m := range d.Matches {
		fmt.Fprintf(&sb, "%s:%d: %s\n", m.File, m.LineNumber, m.LineContent)
	}
	return strings.TrimSpace(sb.String()), nil
}

func (s *SDKSandboxClient) FileFind(path, glob string) (string, error) {
	ctx := context.Background()
	resp, err := s.client.File.FindFiles(ctx, &api.FileFindRequest{
		Path: path,
		Glob: glob,
	})
	if err != nil {
		return "", fmt.Errorf("find failed: %w", err)
	}
	if resp.Data == nil || len(resp.Data.Files) == 0 {
		return "No files found.", nil
	}
	return strings.Join(resp.Data.Files, "\n"), nil
}

// FileListPath lists the contents of a directory.
func (s *SDKSandboxClient) FileListPath(path string, recursive bool, showHidden bool, maxDepth *int) (string, error) {
	ctx := context.Background()
	req := &api.FileListRequest{
		Path:        path,
		IncludeSize: api.Bool(true),
	}
	if recursive {
		req.Recursive = api.Bool(true)
	}
	if showHidden {
		req.ShowHidden = api.Bool(true)
	}
	if maxDepth != nil {
		req.MaxDepth = maxDepth
	}

	resp, err := s.client.File.ListPath(ctx, req)
	if err != nil {
		return "", fmt.Errorf("list failed: %w", err)
	}
	if resp.Data == nil {
		return "Empty directory.", nil
	}
	data, _ := json.MarshalIndent(resp.Data, "", "  ")
	return string(data), nil
}

// FileStrReplaceEditor performs precise file editing using the StrReplaceEditor API.
// Supports: view, str_replace, create, insert, undo_edit.
func (s *SDKSandboxClient) FileStrReplaceEditor(command, path string, opts map[string]interface{}) (string, error) {
	ctx := context.Background()

	var cmd api.Command
	switch command {
	case "view":
		cmd = api.CommandView
	case "str_replace":
		cmd = api.CommandStrReplace
	case "create":
		cmd = api.CommandCreate
	case "insert":
		cmd = api.CommandInsert
	case "undo_edit":
		cmd = api.CommandUndoEdit
	default:
		return "", fmt.Errorf("invalid command: %s (use view/str_replace/create/insert/undo_edit)", command)
	}

	req := &api.StrReplaceEditorRequest{
		Command: cmd,
		Path:    path,
	}

	if v, ok := opts["old_str"].(string); ok {
		req.OldStr = &v
	}
	if v, ok := opts["new_str"].(string); ok {
		req.NewStr = &v
	}
	if v, ok := opts["file_text"].(string); ok {
		req.FileText = &v
	}
	if v, ok := opts["insert_line"].(float64); ok {
		n := int(v)
		req.InsertLine = &n
	}
	if v, ok := opts["view_range"].([]interface{}); ok && len(v) == 2 {
		if start, ok := v[0].(float64); ok {
			if end, ok := v[1].(float64); ok {
				req.ViewRange = []int{int(start), int(end)}
			}
		}
	}

	resp, err := s.client.File.StrReplaceEditor(ctx, req)
	if err != nil {
		return "", fmt.Errorf("file_edit %s failed: %w", command, err)
	}
	if resp.Data == nil {
		return "OK", nil
	}
	data, _ := json.MarshalIndent(resp.Data, "", "  ")
	return string(data), nil
}

// --- #6: Shell sessions ---

func (s *SDKSandboxClient) ShellCreateSession() (string, error) {
	ctx := context.Background()
	resp, err := s.client.Shell.CreateSession(ctx, &api.ShellCreateSessionRequest{})
	if err != nil {
		return "", fmt.Errorf("failed to create shell session: %w", err)
	}
	if resp.Data == nil {
		return "", fmt.Errorf("create session returned nil data")
	}
	data, _ := json.MarshalIndent(resp.Data, "", "  ")
	return string(data), nil
}

func (s *SDKSandboxClient) ShellExecInSession(sessionID, command string, asyncMode bool, timeout float64) (string, error) {
	ctx := context.Background()
	req := &api.ShellExecRequest{
		Command: command,
	}
	if sessionID != "" {
		req.Id = &sessionID
	}
	if asyncMode {
		req.AsyncMode = api.Bool(true)
	}
	if timeout > 0 {
		req.Timeout = &timeout
	}

	resp, err := s.client.Shell.ExecCommand(ctx, req)
	if err != nil {
		return "", fmt.Errorf("shell exec failed: %w", err)
	}
	if resp.Data == nil {
		return "", fmt.Errorf("shell exec returned nil data")
	}
	d := resp.Data
	var sb strings.Builder
	fmt.Fprintf(&sb, "SessionID: %s\nStatus: %s\n", d.SessionId, d.Status)
	if d.Output != nil && *d.Output != "" {
		fmt.Fprintf(&sb, "\n%s\n", *d.Output)
	}
	if d.ExitCode != nil {
		fmt.Fprintf(&sb, "\nExit code: %d\n", *d.ExitCode)
	}
	return strings.TrimSpace(sb.String()), nil
}

func (s *SDKSandboxClient) ShellViewSession(sessionID string) (string, error) {
	ctx := context.Background()
	resp, err := s.client.Shell.View(ctx, &api.ShellViewRequest{
		Id: sessionID,
	})
	if err != nil {
		return "", fmt.Errorf("shell view failed: %w", err)
	}
	data, _ := json.MarshalIndent(resp.Data, "", "  ")
	return string(data), nil
}

func (s *SDKSandboxClient) ShellKillProcess(sessionID string) (string, error) {
	ctx := context.Background()
	_, err := s.client.Shell.KillProcess(ctx, &api.ShellKillProcessRequest{
		Id: sessionID,
	})
	if err != nil {
		return "", fmt.Errorf("shell kill failed: %w", err)
	}
	return "Process killed.", nil
}

func (s *SDKSandboxClient) ShellListSessions() (string, error) {
	ctx := context.Background()
	resp, err := s.client.Shell.ListSessions(ctx)
	if err != nil {
		return "", fmt.Errorf("shell list failed: %w", err)
	}
	data, _ := json.MarshalIndent(resp.Data, "", "  ")
	return string(data), nil
}

func (s *SDKSandboxClient) ShellWriteToProcess(sessionID, input string) (string, error) {
	ctx := context.Background()
	_, err := s.client.Shell.WriteToProcess(ctx, &api.ShellWriteToProcessRequest{
		Id:    sessionID,
		Input: input,
	})
	if err != nil {
		return "", fmt.Errorf("shell write failed: %w", err)
	}
	return "Input sent.", nil
}

// --- #6: Display recording ---

func (s *SDKSandboxClient) DisplayRecord(action string) (string, error) {
	ctx := context.Background()
	var a api.DisplayRecordRequestAction
	switch action {
	case "start":
		a = api.DisplayRecordRequestActionStart
	case "stop":
		a = api.DisplayRecordRequestActionStop
	case "status":
		a = api.DisplayRecordRequestActionStatus
	default:
		return "", fmt.Errorf("invalid display action: %s (use start/stop/status)", action)
	}

	resp, err := s.client.Display.Record(ctx, &api.DisplayRecordRequest{
		Action: a,
	})
	if err != nil {
		return "", fmt.Errorf("display record failed: %w", err)
	}
	if resp.Data == nil {
		return "OK", nil
	}
	d := resp.Data
	var sb strings.Builder
	fmt.Fprintf(&sb, "Status: %s\n", d.Status)
	if d.SavePath != nil {
		fmt.Fprintf(&sb, "Path: %s\n", *d.SavePath)
	}
	if d.Duration != nil {
		fmt.Fprintf(&sb, "Duration: %.1fs\n", *d.Duration)
	}
	if d.FileSizeBytes != nil {
		fmt.Fprintf(&sb, "Size: %d bytes\n", *d.FileSizeBytes)
	}
	return strings.TrimSpace(sb.String()), nil
}

// --- Helpers ---

// DownloadFile downloads a file from the sandbox as raw bytes.
func (s *SDKSandboxClient) DownloadFile(path string) (io.Reader, error) {
	ctx := context.Background()
	reader, err := s.client.File.DownloadFile(ctx, &api.FileDownloadFileRequest{
		Path: path,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to download file: %w", err)
	}
	return reader, nil
}

// UploadFile uploads an io.Reader to the sandbox at the given destination path.
// If destPath is empty, the file is placed in /tmp/<filename>.
func (s *SDKSandboxClient) UploadFile(r io.Reader, destPath string) (string, error) {
	ctx := context.Background()
	// Derive filename from destPath so the SDK multipart writer sets filename on the part.
	filename := filepath.Base(destPath)
	if filename == "" || filename == "." {
		filename = "upload"
	}
	req := &api.BodyUploadFile{File: &namedReader{Reader: r, name: filename}}
	if destPath != "" {
		req.Path = &destPath
	}
	resp, err := s.client.File.UploadFile(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to upload file: %w", err)
	}
	if resp == nil || resp.Data == nil {
		return "", fmt.Errorf("upload returned empty response")
	}
	d := resp.Data
	if !d.GetSuccess() {
		return "", fmt.Errorf("upload failed (success=false)")
	}
	return d.GetFilePath(), nil
}

func (s *SDKSandboxClient) writeFileBytes(path string, data []byte) error {
	ctx := context.Background()
	enc := api.FileContentEncodingBase64
	encoded := base64.StdEncoding.EncodeToString(data)
	_, err := s.client.File.WriteFile(ctx, &api.FileWriteRequest{
		File:     path,
		Content:  encoded,
		Encoding: &enc,
	})
	return err
}

func sdkSbxPreviewHost(s *SDKSandboxClient) string {
	host := strings.TrimPrefix(s.BaseURL, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimSuffix(host, "/")
	return host
}
