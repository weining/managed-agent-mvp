package main

import (
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"managed-agent/llm"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	// Load configuration: config file → env vars → defaults
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("Invalid config: %v", err)
	}

	// Setup log file: write to both stdout and data/agent.log
	logDir := filepath.Dir(cfg.DataDir) // "data"
	os.MkdirAll(logDir, 0755)
	logFile, err := os.OpenFile(filepath.Join(logDir, "agent.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}
	defer logFile.Close()
	log.SetOutput(io.MultiWriter(os.Stdout, logFile))

	// Initialize session store (file-based)
	store, err := NewSessionStore(cfg.DataDir)
	if err != nil {
		log.Fatalf("Failed to init session store: %v", err)
	}

	// Initialize sandbox client (SDK-based)
	sbx := NewSDKSandboxClient(cfg.SandboxBaseURL, cfg.SandboxID)
	if err := sbx.Init(); err != nil {
		log.Fatalf("Failed to init sandbox: %v", err)
	}
	log.Printf("Sandbox ready: %s via %s", sbx.SandboxID, sbx.BaseURL)

	// Initialize LLM client
	llmClient, err := llm.New(llm.ParseConfig(
		cfg.LLMProvider, cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel, cfg.LLMMaxTokens,
		cfg.LLMCustomHeader, cfg.LLMDebug == "true",
	))
	if err != nil {
		log.Fatalf("Failed to init LLM client: %v", err)
	}
	log.Printf("LLM ready: provider=%s model=%s", cfg.LLMProvider, cfg.LLMModel)

	// Load skills
	skills := NewSkillRegistry()
	if err := skills.LoadDir(cfg.SkillsDir); err != nil {
		log.Fatalf("Failed to load skills: %v", err)
	}
	log.Printf("Skills loaded: %d from %s", len(skills.List()), cfg.SkillsDir)

	// Initialize memory store
	memoryPath := filepath.Join(filepath.Dir(cfg.DataDir), "memory.json")
	memoryStore, err := NewFileMemoryStore(memoryPath)
	if err != nil {
		log.Fatalf("Failed to init memory store: %v", err)
	}
	log.Printf("Memory store ready: %s", memoryPath)

	deps := &AgentDeps{
		Store:       store,
		Sandbox:     sbx,
		Claude:      llmClient,
		Skills:      skills,
		Config:      cfg,
		ImageCache:  NewImageCache(),
		MemoryStore: memoryStore,
	}

	// Setup routes and start server
	handler := setupRoutes(deps)
	log.Printf("Server starting on %s", cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, handler); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
