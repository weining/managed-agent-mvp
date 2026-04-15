package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// GeminiClient implements LLMClient using Google Gemini streamGenerateContent API.
type GeminiClient struct {
	BaseURL   string
	APIKey    string
	Model     string
	MaxTokens int
}

func NewGeminiClient(cfg *Config) *GeminiClient {
	maxTokens := 8192
	if cfg.LLMMaxTokens != "" {
		if v, err := strconv.Atoi(cfg.LLMMaxTokens); err == nil && v > 0 {
			maxTokens = v
		}
	}
	return &GeminiClient{
		BaseURL:   cfg.LLMBaseURL,
		APIKey:    cfg.LLMAPIKey,
		Model:     cfg.LLMModel,
		MaxTokens: maxTokens,
	}
}

// --- Gemini API request types ---

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text         string              `json:"text,omitempty"`
	FunctionCall *geminiFunctionCall `json:"functionCall,omitempty"`
	FunctionResp *geminiFunctionResp `json:"functionResponse,omitempty"`
}

type geminiFunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

type geminiFunctionResp struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFuncDecl `json:"functionDeclarations"`
}

type geminiFuncDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type geminiRequest struct {
	Contents          []geminiContent `json:"contents"`
	SystemInstruction *geminiContent  `json:"systemInstruction,omitempty"`
	Tools             []geminiTool    `json:"tools,omitempty"`
	GenerationConfig  geminiGenConfig `json:"generationConfig"`
}

type geminiGenConfig struct {
	MaxOutputTokens int `json:"maxOutputTokens"`
}

// --- Gemini SSE response types ---

type geminiSSEChunk struct {
	Candidates []geminiCandidate `json:"candidates"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
}

// CallStream implements LLMClient.
func (c *GeminiClient) CallStream(system string, messages []ClaudeMessage, tools []ClaudeTool, cb StreamCallback) (*ClaudeResponse, error) {
	// Build contents from ClaudeMessages
	var contents []geminiContent
	for _, msg := range messages {
		role := msg.Role
		if role == "assistant" {
			role = "model"
		}
		var parts []geminiPart
		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				parts = append(parts, geminiPart{Text: block.Text})
			case "tool_use":
				args := toGeminiArgs(block.Input)
				parts = append(parts, geminiPart{
					FunctionCall: &geminiFunctionCall{Name: block.Name, Args: args},
				})
			case "tool_result":
				parts = append(parts, geminiPart{
					FunctionResp: &geminiFunctionResp{
						Name:     block.ToolUseID,
						Response: map[string]interface{}{"output": block.Content},
					},
				})
			}
		}
		if len(parts) > 0 {
			contents = append(contents, geminiContent{Role: role, Parts: parts})
		}
	}

	// Build tools
	var gemTools []geminiTool
	if len(tools) > 0 {
		var decls []geminiFuncDecl
		for _, t := range tools {
			// Rewrite input_schema to parameters with uppercase types
			params := rewriteSchemaTypes(t.InputSchema)
			decls = append(decls, geminiFuncDecl{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			})
		}
		gemTools = []geminiTool{{FunctionDeclarations: decls}}
	}

	// Build request
	reqBody := geminiRequest{
		Contents:         contents,
		Tools:            gemTools,
		GenerationConfig: geminiGenConfig{MaxOutputTokens: c.MaxTokens},
	}
	if system != "" {
		reqBody.SystemInstruction = &geminiContent{
			Parts: []geminiPart{{Text: system}},
		}
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal gemini request: %w", err)
	}

	url := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?key=%s&alt=sse",
		c.BaseURL, c.Model, c.APIKey)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create gemini request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call gemini: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gemini API error, status=%d, body=%s", resp.StatusCode, string(body))
	}

	// Parse SSE stream
	result := &ClaudeResponse{StopReason: "end_turn"}
	var textBuf strings.Builder
	toolIndex := 0
	var toolBlocks []ContentBlock

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		dataStr := strings.TrimPrefix(line, "data: ")
		if dataStr == "[DONE]" {
			break
		}

		var chunk geminiSSEChunk
		if err := json.Unmarshal([]byte(dataStr), &chunk); err != nil {
			continue
		}

		for _, cand := range chunk.Candidates {
			for _, part := range cand.Content.Parts {
				if part.Text != "" {
					textBuf.WriteString(part.Text)
					cb("text", part.Text)
				}
				if part.FunctionCall != nil {
					id := fmt.Sprintf("gemini-%s-%d", part.FunctionCall.Name, toolIndex)
					toolIndex++
					cb("tool_use_start", map[string]interface{}{
						"index": len(toolBlocks),
						"id":    id,
						"name":  part.FunctionCall.Name,
					})
					inputJSON, _ := json.Marshal(part.FunctionCall.Args)
					var input interface{}
					json.Unmarshal(inputJSON, &input)
					cb("tool_use", map[string]interface{}{
						"index": len(toolBlocks),
						"id":    id,
						"name":  part.FunctionCall.Name,
						"input": input,
					})
					toolBlocks = append(toolBlocks, ContentBlock{
						Type:  "tool_use",
						ID:    id,
						Name:  part.FunctionCall.Name,
						Input: input,
					})
				}
			}
			if cand.FinishReason != "" && cand.FinishReason != "STOP" {
				result.StopReason = strings.ToLower(cand.FinishReason)
			}
		}
	}

	// Assemble content blocks
	if textBuf.Len() > 0 {
		result.Content = append(result.Content, ContentBlock{
			Type: "text",
			Text: textBuf.String(),
		})
	}
	result.Content = append(result.Content, toolBlocks...)
	if len(toolBlocks) > 0 {
		result.StopReason = "tool_use"
	}

	return result, nil
}

// rewriteSchemaTypes converts an Anthropic input_schema JSON to Gemini parameters
// format, uppercasing the "type" field values (e.g. "object" -> "OBJECT").
func rewriteSchemaTypes(raw json.RawMessage) json.RawMessage {
	var obj interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw
	}
	rewriteTypes(obj)
	result, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return result
}

func rewriteTypes(v interface{}) {
	m, ok := v.(map[string]interface{})
	if !ok {
		return
	}
	if t, ok := m["type"].(string); ok {
		m["type"] = strings.ToUpper(t)
	}
	for _, val := range m {
		switch child := val.(type) {
		case map[string]interface{}:
			rewriteTypes(child)
		case []interface{}:
			for _, item := range child {
				rewriteTypes(item)
			}
		}
	}
}

func toGeminiArgs(input interface{}) map[string]interface{} {
	if m, ok := input.(map[string]interface{}); ok {
		return m
	}
	data, _ := json.Marshal(input)
	var m map[string]interface{}
	json.Unmarshal(data, &m)
	return m
}
