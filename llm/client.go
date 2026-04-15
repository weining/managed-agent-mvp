package llm

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// LLMClient is the unified interface for all LLM providers.
type LLMClient interface {
	CallStream(system string, messages []ClaudeMessage, tools []ClaudeTool, cb StreamCallback) (*ClaudeResponse, error)
}

// StreamCallback receives streaming events from an LLM provider.
type StreamCallback func(eventType string, data interface{})

// Config holds the parameters needed to construct an LLM client.
type Config struct {
	Provider  string
	BaseURL   string
	APIKey    string
	Model     string
	MaxTokens int
}

// ParseConfig converts string-based config values into a typed Config.
func ParseConfig(provider, baseURL, apiKey, model, maxTokensStr string) Config {
	maxTokens := 8192
	if maxTokensStr != "" {
		if v, err := strconv.Atoi(maxTokensStr); err == nil && v > 0 {
			maxTokens = v
		}
	}
	return Config{
		Provider:  provider,
		BaseURL:   baseURL,
		APIKey:    apiKey,
		Model:     model,
		MaxTokens: maxTokens,
	}
}

// New creates an LLMClient based on cfg.Provider.
// Valid providers: "" or "claude" (default), "gemini", "openai".
func New(cfg Config) (LLMClient, error) {
	switch cfg.Provider {
	case "", "claude":
		return newClaudeClient(cfg), nil
	case "gemini":
		return newGeminiClient(cfg), nil
	case "openai":
		return newOpenAIClient(cfg), nil
	default:
		return nil, fmt.Errorf("unknown llm_provider: %q (valid: claude, gemini, openai)", cfg.Provider)
	}
}

// --- Shared message types (internal canonical format) ---

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
