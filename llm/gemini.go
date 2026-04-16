package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
)

// GeminiClient implements LLMClient using Google Gemini streamGenerateContent API.
type GeminiClient struct {
	BaseURL   string
	APIKey    string
	Model     string
	MaxTokens int
}

func newGeminiClient(cfg Config) *GeminiClient {
	return &GeminiClient{
		BaseURL:   cfg.BaseURL,
		APIKey:    cfg.APIKey,
		Model:     cfg.Model,
		MaxTokens: cfg.MaxTokens,
	}
}

// --- Gemini API request types ---

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text               string              `json:"text,omitempty"`
	FunctionCall       *geminiFunctionCall `json:"functionCall,omitempty"`
	FunctionResp       *geminiFunctionResp `json:"functionResponse,omitempty"`
	ThoughtSignature   string              `json:"thoughtSignature,omitempty"`
	ThoughtSignatureSC string              `json:"thought_signature,omitempty"` // snake_case variant
}

// getThoughtSignature returns the thought signature from whichever field is set.
func (p geminiPart) getThoughtSignature() string {
	if p.ThoughtSignature != "" {
		return p.ThoughtSignature
	}
	return p.ThoughtSignatureSC
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

type geminiSSEChunk struct {
	Candidates []geminiCandidate `json:"candidates"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
}

// geminiToolIDCounter ensures unique tool call IDs across agent loop rounds.
var geminiToolIDCounter uint64

// CallStream implements LLMClient.
func (c *GeminiClient) CallStream(system string, messages []ClaudeMessage, tools []ClaudeTool, cb StreamCallback) (*ClaudeResponse, error) {
	// Build a map of tool-use ID -> function name from assistant messages,
	// needed to populate functionResponse.name correctly (Gemini requires
	// the original function name, not the synthetic tool-use ID).
	toolIDToName := map[string]string{}
	for _, msg := range messages {
		if msg.Role == "assistant" {
			for _, block := range msg.Content {
				if block.Type == "tool_use" {
					toolIDToName[block.ID] = block.Name
				}
			}
		}
	}

	// Build contents from ClaudeMessages.
	// Gemini requires that functionResponse parts and plain-text parts appear
	// in separate content turns — mixing them in one turn is a protocol error.
	var contents []geminiContent
	for _, msg := range messages {
		role := msg.Role
		if role == "assistant" {
			role = "model"
		}

		if role == "user" {
			// Split into functionResponse parts and text parts so they end up
			// in separate content turns (Gemini rejects mixed turns).
			var funcRespParts []geminiPart
			var textParts []geminiPart
			for _, block := range msg.Content {
				switch block.Type {
				case "text":
					textParts = append(textParts, geminiPart{Text: block.Text})
				case "tool_result":
					name := toolIDToName[block.ToolUseID]
					if name == "" {
						name = block.ToolUseID // fallback
					}
					funcRespParts = append(funcRespParts, geminiPart{
						FunctionResp: &geminiFunctionResp{
							Name:     name,
							Response: map[string]interface{}{"output": block.Content},
						},
					})
				}
			}
			if len(funcRespParts) > 0 {
				contents = append(contents, geminiContent{Role: "user", Parts: funcRespParts})
			}
			if len(textParts) > 0 {
				contents = append(contents, geminiContent{Role: "user", Parts: textParts})
			}
		} else {
			// model (assistant) turn
			var parts []geminiPart
			for _, block := range msg.Content {
				switch block.Type {
				case "text":
					parts = append(parts, geminiPart{Text: block.Text})
				case "tool_use":
					// thoughtSignature must be on the same Part as functionCall,
					// not a separate part — Gemini requires it for history replay.
					// Set both camelCase and snake_case fields for compatibility.
					args := toGeminiArgs(block.Input)
					parts = append(parts, geminiPart{
						FunctionCall:       &geminiFunctionCall{Name: block.Name, Args: args},
						ThoughtSignature:   block.ThoughtSignature,
						ThoughtSignatureSC: block.ThoughtSignature,
					})
				}
			}
			if len(parts) > 0 {
				contents = append(contents, geminiContent{Role: role, Parts: parts})
			}
		}
	}

	// Build tools
	var gemTools []geminiTool
	if len(tools) > 0 {
		var decls []geminiFuncDecl
		for _, t := range tools {
			params := rewriteSchemaTypes(t.InputSchema)
			decls = append(decls, geminiFuncDecl{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			})
		}
		gemTools = []geminiTool{{FunctionDeclarations: decls}}
	}

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

	url := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?alt=sse",
		c.BaseURL, c.Model)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create gemini request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", c.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call gemini: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gemini API error, status=%d, body=%s", resp.StatusCode, string(body))
	}

	result := &ClaudeResponse{StopReason: "end_turn"}
	var textBuf strings.Builder
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
					seq := atomic.AddUint64(&geminiToolIDCounter, 1)
					id := fmt.Sprintf("gemini-%s-%d", part.FunctionCall.Name, seq)
					thoughtSig := part.getThoughtSignature()
					log.Printf("[gemini] functionCall=%s thoughtSig_len=%d raw_chunk_snippet=%.200s",
						part.FunctionCall.Name, len(thoughtSig), dataStr)
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
					// thoughtSignature is on the same Part as functionCall
					// (not a separate part) — must be preserved for history replay.
					toolBlocks = append(toolBlocks, ContentBlock{
						Type:             "tool_use",
						ID:               id,
						Name:             part.FunctionCall.Name,
						Input:            input,
						ThoughtSignature: part.getThoughtSignature(),
					})
				}
			}
			if cand.FinishReason != "" && cand.FinishReason != "STOP" {
				result.StopReason = strings.ToLower(cand.FinishReason)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("gemini stream read error: %w", err)
	}

	if textBuf.Len() > 0 {
		result.Content = append(result.Content, ContentBlock{Type: "text", Text: textBuf.String()})
	}
	result.Content = append(result.Content, toolBlocks...)
	if len(toolBlocks) > 0 {
		result.StopReason = "tool_use"
	}

	return result, nil
}

// rewriteSchemaTypes uppercases "type" values in a JSON schema for Gemini
// (e.g. "object" -> "OBJECT").
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
