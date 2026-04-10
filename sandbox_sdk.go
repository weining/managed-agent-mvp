package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	api "github.com/agent-infra/sandbox-sdk-go"
	"github.com/agent-infra/sandbox-sdk-go/client"
	"github.com/agent-infra/sandbox-sdk-go/option"
)

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
	// Use a lightweight file listing to verify connectivity.
	// Avoid Shell.ListSessions which has a time-parsing bug in the SDK.
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

// --- #1: browse_web via shell (BrowserPage API not available on this sandbox) ---

func (s *SDKSandboxClient) BrowseWebSDK(targetURL string) (string, error) {
	// Use Python + CDP via heredoc to avoid quoting issues
	script := fmt.Sprintf(`python3 << 'PYEOF'
import json, time, urllib.request, websocket

target_url = %q

targets = json.loads(urllib.request.urlopen('http://localhost:9222/json').read())
ws_url = next((t['webSocketDebuggerUrl'] for t in targets if t.get('type') == 'page'), None)
if not ws_url:
    print(json.dumps({'error': 'No browser page target'}))
    exit(1)

ws = websocket.create_connection(ws_url, timeout=30)
_id = [0]
def cdp(method, params=None):
    _id[0] += 1
    msg = {'id': _id[0], 'method': method}
    if params: msg['params'] = params
    ws.send(json.dumps(msg))
    while True:
        r = json.loads(ws.recv())
        if r.get('id') == _id[0]: return r.get('result', {})

cdp('Page.navigate', {'url': target_url})
time.sleep(4)

title = cdp('Runtime.evaluate', {'expression': 'document.title'}).get('result', {}).get('value', '')
content = cdp('Runtime.evaluate', {'expression': 'document.body.innerText.substring(0, 12000)'}).get('result', {}).get('value', '')
ws.close()
print(json.dumps({'title': title, 'content': content}, ensure_ascii=False))
PYEOF`, targetURL)

	stdout, exitCode, err := s.ExecCommand(script)
	if err != nil {
		return "", fmt.Errorf("browse_web failed: %w", err)
	}
	stdout = strings.TrimSpace(stdout)
	if exitCode != 0 {
		return "", fmt.Errorf("browse_web exit %d: %s", exitCode, stdout)
	}

	var result struct {
		Title   string `json:"title"`
		Content string `json:"content"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		return stdout, nil
	}
	if result.Error != "" {
		return "", fmt.Errorf("browse_web error: %s", result.Error)
	}

	var sb strings.Builder
	if result.Title != "" {
		fmt.Fprintf(&sb, "Title: %s\n", result.Title)
	}
	fmt.Fprintf(&sb, "URL: %s\n\n%s", targetURL, result.Content)
	return sb.String(), nil
}

// --- #1: take_screenshot via SDK (Browser.Screenshot is available) ---

func (s *SDKSandboxClient) TakeScreenshotSDK(targetURL string) (string, error) {
	ctx := context.Background()

	// Navigate via CDP if URL is provided
	if targetURL != "" {
		script := fmt.Sprintf(`python3 << 'PYEOF'
import json, time, urllib.request, websocket
target_url = %q
targets = json.loads(urllib.request.urlopen('http://localhost:9222/json').read())
ws_url = next((t['webSocketDebuggerUrl'] for t in targets if t.get('type') == 'page'), None)
if not ws_url: exit(1)
ws = websocket.create_connection(ws_url, timeout=30)
ws.send(json.dumps({'id':1,'method':'Page.navigate','params':{'url':target_url}}))
ws.recv()
time.sleep(3)
ws.close()
PYEOF`, targetURL)
		s.ExecCommand(script)
	}

	// Use the working /v1/browser/screenshot endpoint
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
	// Convert map[string]interface{} to map[string]any (same type, just explicit)
	resp, err := s.client.Mcp.ExecuteMcpTool(ctx, serverName, toolName, args)
	if err != nil {
		return "", fmt.Errorf("failed to call MCP tool %s/%s: %w", serverName, toolName, err)
	}
	if resp.Data == nil {
		return "MCP tool returned no result.", nil
	}
	if resp.Data.IsError != nil && *resp.Data.IsError {
		// Extract error text from content
		for _, item := range resp.Data.Content {
			if item.Text != nil {
				return item.Text.Text, fmt.Errorf("MCP tool error: %s", item.Text.Text)
			}
		}
		return "MCP tool returned an error.", fmt.Errorf("MCP tool returned an error")
	}
	// Collect text content
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
	// Fallback: serialize structured content
	if resp.Data.StructuredContent != nil {
		data, _ := json.MarshalIndent(resp.Data.StructuredContent, "", "  ")
		return string(data), nil
	}
	data, _ := json.MarshalIndent(resp.Data, "", "  ")
	return string(data), nil
}

// --- #3: Browser interaction via CDP shell commands ---
// BrowserPage API (/v1/browser/page/*) is not available on this sandbox version.
// Use CDP via Python websocket scripts as fallback.

func (s *SDKSandboxClient) BrowserClick(selector string, index *int, x, y *float64) (string, error) {
	if x != nil && y != nil {
		script := fmt.Sprintf(`python3 << 'PYEOF'
import json, urllib.request, websocket
targets = json.loads(urllib.request.urlopen('http://localhost:9222/json').read())
ws_url = next((t['webSocketDebuggerUrl'] for t in targets if t.get('type') == 'page'), None)
if not ws_url: print('Error: No browser page'); exit(1)
ws = websocket.create_connection(ws_url, timeout=10)
def cdp(mid, method, params=None):
    msg = {'id': mid, 'method': method}
    if params: msg['params'] = params
    ws.send(json.dumps(msg))
    while True:
        r = json.loads(ws.recv())
        if r.get('id') == mid: return r.get('result', {})
cdp(1, 'Input.dispatchMouseEvent', {'type':'mousePressed','x':%.1f,'y':%.1f,'button':'left','clickCount':1})
cdp(2, 'Input.dispatchMouseEvent', {'type':'mouseReleased','x':%.1f,'y':%.1f,'button':'left','clickCount':1})
ws.close()
print('Clicked at (%.1f, %.1f)')
PYEOF`, *x, *y, *x, *y, *x, *y)
		stdout, _, err := s.ExecCommand(script)
		if err != nil {
			return "", fmt.Errorf("click failed: %w", err)
		}
		return strings.TrimSpace(stdout), nil
	}
	if selector != "" {
		js := fmt.Sprintf(`document.querySelector(%q).click(); "clicked"`, selector)
		return s.BrowserEvaluate(js)
	}
	return "", fmt.Errorf("provide selector or x/y coordinates for click")
}

func (s *SDKSandboxClient) BrowserFill(selector string, index *int, text string) (string, error) {
	if selector == "" {
		return "", fmt.Errorf("selector is required for fill")
	}
	js := fmt.Sprintf(`(function(){var el=document.querySelector('%s');if(!el)return 'Element not found';el.focus();el.value=%q;el.dispatchEvent(new Event('input',{bubbles:true}));return 'Filled'})()`, selector, text)
	return s.BrowserEvaluate(js)
}

func (s *SDKSandboxClient) BrowserEvaluate(expression string) (string, error) {
	// Write the expression to a temp file to avoid all quoting issues
	tmpPath := fmt.Sprintf("/tmp/_eval_%d.py", time.Now().UnixNano())
	pyCode := fmt.Sprintf(`import json, urllib.request, websocket, sys
targets = json.loads(urllib.request.urlopen('http://localhost:9222/json').read())
ws_url = next((t['webSocketDebuggerUrl'] for t in targets if t.get('type') == 'page'), None)
if not ws_url: print('Error: No browser page'); sys.exit(1)
ws = websocket.create_connection(ws_url, timeout=15)
expr = %q
ws.send(json.dumps({'id':1,'method':'Runtime.evaluate','params':{'expression':expr,'returnByValue':True}}))
while True:
    r = json.loads(ws.recv())
    if r.get('id') == 1:
        result = r.get('result',{}).get('result',{})
        if 'value' in result:
            v = result['value']
            print(json.dumps(v, ensure_ascii=False, default=str) if not isinstance(v, str) else v)
        elif result.get('type') == 'undefined':
            print('undefined')
        else:
            print(json.dumps(result, ensure_ascii=False))
        break
ws.close()
`, expression)

	if err := s.WriteFile(tmpPath, pyCode); err != nil {
		return "", fmt.Errorf("evaluate: failed to write script: %w", err)
	}
	stdout, _, err := s.ExecCommand("python3 " + tmpPath)
	if err != nil {
		return "", fmt.Errorf("evaluate failed: %w", err)
	}
	return strings.TrimSpace(stdout), nil
}

func (s *SDKSandboxClient) BrowserGetElements() (string, error) {
	js := `(function(){
var els=document.querySelectorAll('a,button,input,select,textarea,[role=button],[onclick]');
var result=[];
for(var i=0;i<Math.min(els.length,50);i++){
  var el=els[i];
  var r=el.getBoundingClientRect();
  result.push({tag:el.tagName,type:el.type||'',text:(el.innerText||el.value||'').substring(0,80),
    selector:el.id?'#'+el.id:(el.className?el.tagName.toLowerCase()+'.'+el.className.split(' ')[0]:''),
    rect:{x:Math.round(r.x),y:Math.round(r.y),w:Math.round(r.width),h:Math.round(r.height)}});
}
return JSON.stringify(result)})()` // nolint
	result, err := s.BrowserEvaluate(js)
	if err != nil {
		return "", err
	}
	return result, nil
}

func (s *SDKSandboxClient) BrowserScroll(direction string, amount *int) (string, error) {
	pixels := 300
	if amount != nil {
		pixels = *amount * 100
	}
	var js string
	switch direction {
	case "down":
		js = fmt.Sprintf("window.scrollBy(0,%d);'scrolled down'", pixels)
	case "up":
		js = fmt.Sprintf("window.scrollBy(0,-%d);'scrolled up'", pixels)
	case "left":
		js = fmt.Sprintf("window.scrollBy(-%d,0);'scrolled left'", pixels)
	case "right":
		js = fmt.Sprintf("window.scrollBy(%d,0);'scrolled right'", pixels)
	default:
		js = fmt.Sprintf("window.scrollBy(0,%d);'scrolled down'", pixels)
	}
	return s.BrowserEvaluate(js)
}

func (s *SDKSandboxClient) BrowserGetConsole() (string, error) {
	// Console logs require persistent monitoring; provide recent via JS
	js := `(function(){
if(!window.__consoleLogs){window.__consoleLogs=[];
var orig=console.log;console.log=function(){window.__consoleLogs.push(Array.from(arguments).join(' '));orig.apply(console,arguments)};
var origE=console.error;console.error=function(){window.__consoleLogs.push('[ERROR] '+Array.from(arguments).join(' '));origE.apply(console,arguments)};
return 'Console capture started. Call get_console again to see logs.'}
return JSON.stringify(window.__consoleLogs.slice(-20))})()` // nolint
	return s.BrowserEvaluate(js)
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
