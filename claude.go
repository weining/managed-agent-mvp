package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// --- Anthropic Messages API types ---

type ClaudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    string          `json:"system,omitempty"`
	Messages  []ClaudeMessage `json:"messages"`
	Tools     []ClaudeTool    `json:"tools,omitempty"`
	Stream    bool            `json:"stream,omitempty"`
}

type ClaudeMessage struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

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
	Thinking string `json:"thinking,omitempty"`
}

type ClaudeTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type ClaudeResponse struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      *Usage         `json:"usage,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// --- Streaming types ---

type StreamEvent struct {
	Type  string // event type from SSE
	Data  json.RawMessage
	Index int
}

type ContentBlockStart struct {
	Type         string       `json:"type"`
	Index        int          `json:"index"`
	ContentBlock ContentBlock `json:"content_block"`
}

type ContentBlockDelta struct {
	Type  string     `json:"type"`
	Index int        `json:"index"`
	Delta DeltaBlock `json:"delta"`
}

type DeltaBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	JSON string `json:"partial_json,omitempty"`

	// thinking
	Thinking string `json:"thinking,omitempty"`
}

type MessageDelta struct {
	Type  string      `json:"type"`
	Delta MessageStop `json:"delta"`
	Usage *Usage      `json:"usage,omitempty"`
}

type MessageStop struct {
	StopReason string `json:"stop_reason"`
}

// --- Client ---

type ClaudeClient struct {
	BaseURL      string
	APIKey       string
	Model        string
	MaxTokens    int
	CustomHeader string // JSON string for comate_custom_header, empty to skip
}

func NewClaudeClient(cfg *Config) *ClaudeClient {
	maxTokens := 8192
	if cfg.LLMMaxTokens != "" {
		if v, err := strconv.Atoi(cfg.LLMMaxTokens); err == nil && v > 0 {
			maxTokens = v
		}
	}
	return &ClaudeClient{
		BaseURL:      cfg.LLMBaseURL,
		APIKey:       cfg.LLMAPIKey,
		Model:        cfg.LLMModel,
		MaxTokens:    maxTokens,
		CustomHeader: cfg.LLMCustomHeader,
	}
}

// setHeaders applies common headers to an API request.
func (c *ClaudeClient) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	if c.CustomHeader != "" {
		req.Header.Set("comate_custom_header", c.CustomHeader)
	}
}

// Call makes a non-streaming request.
func (c *ClaudeClient) Call(system string, messages []ClaudeMessage, tools []ClaudeTool) (*ClaudeResponse, error) {
	reqBody := ClaudeRequest{
		Model:     c.Model,
		MaxTokens: c.MaxTokens,
		System:    system,
		Messages:  messages,
		Tools:     tools,
		Stream:    false,
	}
	data, _ := json.Marshal(reqBody)

	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/v1/messages", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call claude: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("claude API error, status=%d, body=%s", resp.StatusCode, string(body))
	}

	var result ClaudeResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to decode claude response: %w", err)
	}
	return &result, nil
}

// StreamCallback receives streaming events from Claude.
type StreamCallback func(eventType string, data interface{})

// CallStream makes a streaming request and invokes cb for each event.
func (c *ClaudeClient) CallStream(system string, messages []ClaudeMessage, tools []ClaudeTool, cb StreamCallback) (*ClaudeResponse, error) {
	reqBody := ClaudeRequest{
		Model:     c.Model,
		MaxTokens: c.MaxTokens,
		System:    system,
		Messages:  messages,
		Tools:     tools,
		Stream:    true,
	}
	data, _ := json.Marshal(reqBody)

	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/v1/messages", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call claude stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("claude stream error, status=%d, body=%s", resp.StatusCode, string(body))
	}

	// Parse SSE
	result := &ClaudeResponse{}
	var currentBlocks []ContentBlock
	blockInputJSONs := map[int]*strings.Builder{} // accumulate partial JSON per block index

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	var eventType string

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		dataStr := strings.TrimPrefix(line, "data: ")

		switch eventType {
		case "message_start":
			var msg struct {
				Type    string         `json:"type"`
				Message ClaudeResponse `json:"message"`
			}
			json.Unmarshal([]byte(dataStr), &msg)
			result.ID = msg.Message.ID
			result.Role = msg.Message.Role

		case "content_block_start":
			var cbs ContentBlockStart
			json.Unmarshal([]byte(dataStr), &cbs)
			for len(currentBlocks) <= cbs.Index {
				currentBlocks = append(currentBlocks, ContentBlock{})
			}
			currentBlocks[cbs.Index] = cbs.ContentBlock
			if cbs.ContentBlock.Type == "tool_use" {
				cb("tool_use_start", map[string]interface{}{
					"index": cbs.Index,
					"id":    cbs.ContentBlock.ID,
					"name":  cbs.ContentBlock.Name,
				})
				blockInputJSONs[cbs.Index] = &strings.Builder{}
			} else if cbs.ContentBlock.Type == "thinking" {
				cb("thinking_start", nil)
			}

		case "content_block_delta":
			var cbd ContentBlockDelta
			json.Unmarshal([]byte(dataStr), &cbd)
			switch cbd.Delta.Type {
			case "text_delta":
				if cbd.Index < len(currentBlocks) {
					currentBlocks[cbd.Index].Text += cbd.Delta.Text
				}
				cb("text", cbd.Delta.Text)
			case "thinking_delta":
				if cbd.Index < len(currentBlocks) {
					currentBlocks[cbd.Index].Thinking += cbd.Delta.Thinking
				}
				cb("thinking", cbd.Delta.Thinking)
			case "input_json_delta":
				if sb, ok := blockInputJSONs[cbd.Index]; ok {
					sb.WriteString(cbd.Delta.JSON)
				}
			}

		case "content_block_stop":
			var cbs struct {
				Index int `json:"index"`
			}
			json.Unmarshal([]byte(dataStr), &cbs)
			if sb, ok := blockInputJSONs[cbs.Index]; ok {
				var input interface{}
				raw := sb.String()
				if err := json.Unmarshal([]byte(raw), &input); err != nil {
					log.Printf("Warning: failed to parse tool input JSON (len=%d): %v", len(raw), err)
				}
				currentBlocks[cbs.Index].Input = input
				cb("tool_use", map[string]interface{}{
					"index": cbs.Index,
					"id":    currentBlocks[cbs.Index].ID,
					"name":  currentBlocks[cbs.Index].Name,
					"input": input,
				})
				delete(blockInputJSONs, cbs.Index)
			}

		case "message_delta":
			var md MessageDelta
			json.Unmarshal([]byte(dataStr), &md)
			result.StopReason = md.Delta.StopReason
			if md.Usage != nil {
				result.Usage = md.Usage
			}

		case "message_stop":
			// done
		}
	}

	result.Content = currentBlocks
	return result, nil
}
