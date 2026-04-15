package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAIClient implements LLMClient using OpenAI-compatible chat completions API.
type OpenAIClient struct {
	BaseURL   string
	APIKey    string
	Model     string
	MaxTokens int
}

func newOpenAIClient(cfg Config) *OpenAIClient {
	return &OpenAIClient{
		BaseURL:   cfg.BaseURL,
		APIKey:    cfg.APIKey,
		Model:     cfg.Model,
		MaxTokens: cfg.MaxTokens,
	}
}

// --- OpenAI request/response types ---

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    interface{}   `json:"content,omitempty"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Name       string        `json:"name,omitempty"`
}

type oaiToolCall struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function oaiToolFunction `json:"function"`
}

type oaiToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaiTool struct {
	Type     string      `json:"type"`
	Function oaiFunction `json:"function"`
}

type oaiFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type oaiRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	Messages  []oaiMessage `json:"messages"`
	Tools     []oaiTool    `json:"tools,omitempty"`
	Stream    bool         `json:"stream"`
}

type oaiStreamChunk struct {
	Choices []oaiStreamChoice `json:"choices"`
}

type oaiStreamChoice struct {
	Delta        oaiDelta `json:"delta"`
	FinishReason string   `json:"finish_reason"`
}

type oaiDelta struct {
	Role      string         `json:"role"`
	Content   *string        `json:"content"`
	ToolCalls []oaiDeltaTool `json:"tool_calls"`
}

type oaiDeltaTool struct {
	Index    int          `json:"index"`
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function oaiDeltaFunc `json:"function"`
}

type oaiDeltaFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type toolCallAccum struct {
	id        string
	name      string
	arguments strings.Builder
}

// CallStream implements LLMClient.
func (c *OpenAIClient) CallStream(system string, messages []ClaudeMessage, tools []ClaudeTool, cb StreamCallback) (*ClaudeResponse, error) {
	var oaiMsgs []oaiMessage
	if system != "" {
		oaiMsgs = append(oaiMsgs, oaiMessage{Role: "system", Content: system})
	}

	for _, msg := range messages {
		switch msg.Role {
		case "user":
			// Separate plain text blocks from tool_result blocks.
			// Tool results must appear before any new user text (OpenAI requires
			// role=tool messages immediately after the assistant tool_calls message).
			var textParts []string
			var toolResults []ContentBlock
			for _, block := range msg.Content {
				if block.Type == "text" {
					textParts = append(textParts, block.Text)
				} else if block.Type == "tool_result" {
					toolResults = append(toolResults, block)
				}
			}
			for _, tr := range toolResults {
				oaiMsgs = append(oaiMsgs, oaiMessage{
					Role:       "tool",
					ToolCallID: tr.ToolUseID,
					Content:    tr.Content,
				})
			}
			if len(textParts) > 0 {
				oaiMsgs = append(oaiMsgs, oaiMessage{
					Role:    "user",
					Content: strings.Join(textParts, "\n"),
				})
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

	var oaiTools []oaiTool
	for _, t := range tools {
		oaiTools = append(oaiTools, oaiTool{
			Type: "function",
			Function: oaiFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	reqBody := oaiRequest{
		Model:     c.Model,
		MaxTokens: c.MaxTokens,
		Messages:  oaiMsgs,
		Tools:     oaiTools,
		Stream:    true,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal openai request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/v1/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create openai request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call openai: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai API error, status=%d, body=%s", resp.StatusCode, string(body))
	}

	result := &ClaudeResponse{StopReason: "end_turn"}
	var textBuf strings.Builder
	accums := map[int]*toolCallAccum{}
	var toolBlockOrder []int

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

		var chunk oaiStreamChunk
		if err := json.Unmarshal([]byte(dataStr), &chunk); err != nil {
			continue
		}

		for _, choice := range chunk.Choices {
			delta := choice.Delta
			if delta.Content != nil && *delta.Content != "" {
				textBuf.WriteString(*delta.Content)
				cb("text", *delta.Content)
			}
			for _, tc := range delta.ToolCalls {
				idx := tc.Index
				if _, exists := accums[idx]; !exists {
					accums[idx] = &toolCallAccum{id: tc.ID, name: tc.Function.Name}
					toolBlockOrder = append(toolBlockOrder, idx)
					cb("tool_use_start", map[string]interface{}{
						"index": idx,
						"id":    tc.ID,
						"name":  tc.Function.Name,
					})
				}
				if tc.Function.Name != "" && accums[idx].name == "" {
					accums[idx].name = tc.Function.Name
				}
				accums[idx].arguments.WriteString(tc.Function.Arguments)
			}
			if choice.FinishReason == "tool_calls" {
				result.StopReason = "tool_use"
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("openai stream read error: %w", err)
	}

	if textBuf.Len() > 0 {
		result.Content = append(result.Content, ContentBlock{Type: "text", Text: textBuf.String()})
	}
	for _, idx := range toolBlockOrder {
		acc := accums[idx]
		rawArgs := acc.arguments.String()
		var input interface{}
		json.Unmarshal([]byte(rawArgs), &input)
		cb("tool_use", map[string]interface{}{
			"index": idx,
			"id":    acc.id,
			"name":  acc.name,
			"input": input,
		})
		result.Content = append(result.Content, ContentBlock{
			Type:  "tool_use",
			ID:    acc.id,
			Name:  acc.name,
			Input: input,
		})
	}

	return result, nil
}
