package llm

import (
	"encoding/json"
	"fmt"
	"log"
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
	Debug     bool
}

// ParseConfig converts string-based config values into a typed Config.
func ParseConfig(provider, baseURL, apiKey, model, maxTokensStr string, debug bool) Config {
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
		Debug:     debug,
	}
}

// New creates an LLMClient based on cfg.Provider.
// Valid providers: "" or "claude" (default), "gemini", "openai".
func New(cfg Config) (LLMClient, error) {
	var client LLMClient
	switch cfg.Provider {
	case "", "claude":
		client = newClaudeClient(cfg)
	case "gemini":
		client = newGeminiClient(cfg)
	case "openai":
		client = newOpenAIClient(cfg)
	default:
		return nil, fmt.Errorf("unknown llm_provider: %q (valid: claude, gemini, openai)", cfg.Provider)
	}
	if cfg.Debug {
		client = &debugClient{inner: client}
	}
	return client, nil
}

// debugClient wraps an LLMClient and logs full request/response payloads.
type debugClient struct {
	inner LLMClient
}

func (d *debugClient) CallStream(system string, messages []ClaudeMessage, tools []ClaudeTool, cb StreamCallback) (*ClaudeResponse, error) {
	reqJSON, _ := json.MarshalIndent(map[string]interface{}{
		"system":   system,
		"messages": messages,
		"tools":    len(tools),
	}, "", "  ")
	log.Printf("[LLM DEBUG] === REQUEST ===\n%s", string(reqJSON))

	resp, err := d.inner.CallStream(system, messages, tools, cb)

	if err != nil {
		log.Printf("[LLM DEBUG] === ERROR ===\n%v", err)
	} else {
		respJSON, _ := json.MarshalIndent(resp, "", "  ")
		log.Printf("[LLM DEBUG] === RESPONSE ===\n%s", string(respJSON))
	}
	return resp, err
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

	// gemini-specific: thought signature for tool_use blocks (required for replay)
	ThoughtSignature string `json:"thought_signature,omitempty"`

	// image content block
	ImageMIMEType string `json:"image_mime_type,omitempty"` // e.g. "image/jpeg"
	ImageData     string `json:"image_data,omitempty"`      // base64 encoded bytes
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

// MarshalJSON produces Anthropic-compatible JSON for image blocks.
// For all other block types, the default struct marshaling is used.
func (b ContentBlock) MarshalJSON() ([]byte, error) {
	if b.Type == "image" {
		if b.ImageMIMEType == "" || b.ImageData == "" {
			return nil, fmt.Errorf("ContentBlock type=image requires ImageMIMEType and ImageData")
		}
		return json.Marshal(struct {
			Type   string `json:"type"`
			Source struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			} `json:"source"`
		}{
			Type: "image",
			Source: struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			}{
				Type:      "base64",
				MediaType: b.ImageMIMEType,
				Data:      b.ImageData,
			},
		})
	}
	type alias ContentBlock // avoid infinite recursion
	return json.Marshal(alias(b))
}
