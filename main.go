package main

import (
	"flag"
	"log"
	"net/http"
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

	// Initialize Claude client
	claude := NewClaudeClient(cfg)
	log.Printf("LLM ready: %s via %s", claude.Model, claude.BaseURL)

	// Load skills
	skills := NewSkillRegistry()
	if err := skills.LoadDir(cfg.SkillsDir); err != nil {
		log.Fatalf("Failed to load skills: %v", err)
	}
	log.Printf("Skills loaded: %d from %s", len(skills.List()), cfg.SkillsDir)

	deps := &AgentDeps{
		Store:   store,
		Sandbox: sbx,
		Claude:  claude,
		Skills:  skills,
	}

	// Setup routes and start server
	handler := setupRoutes(deps)
	log.Printf("Server starting on %s", cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, handler); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
