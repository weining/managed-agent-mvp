package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// Config holds all application configuration.
type Config struct {
	// LLM
	LLMBaseURL string `json:"llm_base_url"`

	LLMAPIKey       string `json:"llm_api_key"`
	LLMModel        string `json:"llm_model"`
	LLMMaxTokens    string `json:"llm_max_tokens"`
	LLMCustomHeader string `json:"llm_custom_header"` // JSON string for custom request header
	LLMProvider     string `json:"llm_provider"`

	// Debug
	LLMDebug string `json:"llm_debug"` // "true" to log full LLM request/response

	// Sandbox
	SandboxBaseURL string `json:"sandbox_base_url"`

	SandboxID string `json:"sandbox_id"`

	// Server
	ListenAddr string `json:"listen_addr"`

	// Storage
	DataDir string `json:"data_dir"`

	SkillsDir string `json:"skills_dir"`

	// Agent behavior
	MaxLoopRounds string `json:"max_loop_rounds"` // max tool-use iterations per request, default "50"

	// Memory
	MemoryEventThreshold string `json:"memory_event_threshold"` // event count to trigger summarization, default "40"

	MemoryRecentCount string `json:"memory_recent_count"` // recent events to keep in full, default "20"
}

// DefaultConfig returns configuration with default values.
func DefaultConfig() *Config {
	return &Config{
		LLMBaseURL:           "https://oneapi-comate.baidu-int.com",
		LLMModel:             "Claude Sonnet 4.6",
		LLMMaxTokens:         "8192",
		LLMProvider:          "claude",
		SandboxBaseURL:       "https://8080-t6nk21b8.agent-sandbox.baidu-int.com",
		SandboxID:            "t6nk21b8",
		ListenAddr:           ":8080",
		DataDir:              "data/sessions",
		SkillsDir:            "skills",
		MaxLoopRounds:        "50",
		MemoryEventThreshold: "40",
		MemoryRecentCount:    "20",
	}
}

// LoadConfig loads configuration with the following priority (highest wins):
//  1. Environment variables
//  2. Config file (config.yaml)
//  3. Default values
func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()

	// Layer 2: config file
	if path != "" {
		if err := cfg.loadFile(path); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("failed to load config file %s: %w", path, err)
			}
			// File not found is fine — use defaults + env
		}
	}

	// Layer 1: environment variables override everything
	cfg.applyEnv()

	return cfg, nil
}

// applyEnv overrides config fields with environment variables when set.
func (c *Config) applyEnv() {
	envOverride(&c.LLMBaseURL, "LLM_BASE_URL")
	envOverride(&c.LLMAPIKey, "LLM_API_KEY")
	envOverride(&c.LLMModel, "LLM_MODEL")
	envOverride(&c.LLMMaxTokens, "LLM_MAX_TOKENS")
	envOverride(&c.LLMCustomHeader, "LLM_CUSTOM_HEADER")
	envOverride(&c.LLMProvider, "LLM_PROVIDER")
	envOverride(&c.LLMDebug, "LLM_DEBUG")
	envOverride(&c.SandboxBaseURL, "SANDBOX_BASE_URL")
	envOverride(&c.SandboxID, "SANDBOX_ID")
	envOverride(&c.ListenAddr, "LISTEN_ADDR")
	envOverride(&c.DataDir, "DATA_DIR")
	envOverride(&c.SkillsDir, "SKILLS_DIR")
	envOverride(&c.MaxLoopRounds, "MAX_LOOP_ROUNDS")
	envOverride(&c.MemoryEventThreshold, "MEMORY_EVENT_THRESHOLD")
	envOverride(&c.MemoryRecentCount, "MEMORY_RECENT_COUNT")
}

func envOverride(target *string, key string) {
	if v := os.Getenv(key); v != "" {
		*target = v
	}
}

// loadFile parses a simple YAML-like config file (key: value per line).
// Supports # comments and blank lines.
func (c *Config) loadFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	fields := c.fieldMap()

	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return fmt.Errorf("line %d: invalid format (expected key: value)", i+1)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)

		// Strip optional surrounding quotes
		val = stripQuotes(val)

		if target, exists := fields[key]; exists {
			*target = val
		}
		// Unknown keys are silently ignored for forward compatibility
	}
	return nil
}

// fieldMap returns a mapping from config file key names to their field pointers.
func (c *Config) fieldMap() map[string]*string {
	return map[string]*string{
		"llm_base_url":           &c.LLMBaseURL,
		"llm_api_key":            &c.LLMAPIKey,
		"llm_model":              &c.LLMModel,
		"llm_max_tokens":         &c.LLMMaxTokens,
		"llm_custom_header":      &c.LLMCustomHeader,
		"llm_provider":           &c.LLMProvider,
		"llm_debug":              &c.LLMDebug,
		"sandbox_base_url":       &c.SandboxBaseURL,
		"sandbox_id":             &c.SandboxID,
		"listen_addr":            &c.ListenAddr,
		"data_dir":               &c.DataDir,
		"skills_dir":             &c.SkillsDir,
		"max_loop_rounds":        &c.MaxLoopRounds,
		"memory_event_threshold": &c.MemoryEventThreshold,
		"memory_recent_count":    &c.MemoryRecentCount,
	}
}

// Validate checks required fields.
func (c *Config) Validate() error {
	if c.LLMBaseURL == "" {
		return errors.New("llm_base_url is required")
	}
	if c.LLMAPIKey == "" {
		return errors.New("llm_api_key is required (set in config.yaml or LLM_API_KEY env)")
	}
	if c.SandboxBaseURL == "" {
		return errors.New("sandbox_base_url is required")
	}
	if c.SandboxID == "" {
		return errors.New("sandbox_id is required")
	}
	return nil
}

func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
