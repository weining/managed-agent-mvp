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
)

// --- Anthropic Messages API internal types ---

type claudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    string          `json:"system,omitempty"`
	Messages  []ClaudeMessage `json:"messages"`
	Tools     []ClaudeTool    `json:"tools,omitempty"`
	Stream    bool            `json:"stream,omitempty"`
}

type contentBlockStart struct {
	Type         string       `json:"type"`
	Index        int          `json:"index"`
	ContentBlock ContentBlock `json:"content_block"`
}

type contentBlockDelta struct {
	Type  string     `json:"type"`
	Index int        `json:"index"`
	Delta deltaBlock `json:"delta"`
}

type deltaBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	JSON string `json:"partial_json,omitempty"`

	// thinking
	Thinking string `json:"thinking,omitempty"`
}

type messageDelta struct {
	Type  string      `json:"type"`
	Delta messageStop `json:"delta"`
	Usage *Usage      `json:"usage,omitempty"`
}

type messageStop struct {
	StopReason string `json:"stop_reason"`
}

// ClaudeClient implements LLMClient using the Anthropic Messages API.
type ClaudeClient struct {
	BaseURL      string
	APIKey       string
	Model        string
	MaxTokens    int
	CustomHeader string // JSON object of extra HTTP headers, empty to skip
}

func newClaudeClient(cfg Config) *ClaudeClient {
	return &ClaudeClient{
		BaseURL:      cfg.BaseURL,
		APIKey:       cfg.APIKey,
		Model:        cfg.Model,
		MaxTokens:    cfg.MaxTokens,
		CustomHeader: cfg.CustomHeader,
	}
}

func (c *ClaudeClient) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	if c.CustomHeader != "" {
		var headers map[string]string
		if err := json.Unmarshal([]byte(c.CustomHeader), &headers); err != nil {
			log.Printf("Warning: failed to parse llm_custom_header: %v", err)
			return
		}
		for key, value := range headers {
			if key == "" {
				continue
			}
			req.Header.Set(key, value)
		}
	}
}

// CallStream makes a streaming request and invokes cb for each event.
func (c *ClaudeClient) CallStream(system string, messages []ClaudeMessage, tools []ClaudeTool, cb StreamCallback) (*ClaudeResponse, error) {
	reqBody := claudeRequest{
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
	blockInputJSONs := map[int]*strings.Builder{}

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
			var cbs contentBlockStart
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
			var cbd contentBlockDelta
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
			var md messageDelta
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
