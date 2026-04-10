package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const skillEntryFile = "SKILL.md"

// SkillFile represents a bundled file within a skill (script, reference, asset, etc.).
type SkillFile struct {
	RelPath string `json:"rel_path"` // relative path inside skill dir (e.g. "scripts/setup.sh")
	Content string `json:"content"`
}

// Skill represents a loadable skill definition, compatible with the Claude Code
// skill specification (agentskills.io).
//
// Each skill is a directory containing SKILL.md with YAML frontmatter, plus
// optional subdirectories: scripts/, references/, assets/.
type Skill struct {
	// --- standard fields (agentskills.io spec) ---
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	License       string            `json:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`

	// --- Claude Code extensions ---
	DisableModelInvocation bool   `json:"disable_model_invocation,omitempty"` // only user can invoke
	UserInvocable          *bool  `json:"user_invocable,omitempty"`           // nil=true; false=model only
	Tools                  string `json:"tools,omitempty"`                    // comma-separated tool list
	Context                string `json:"context,omitempty"`                  // "fork" for isolated

	// --- our extensions ---
	Trigger string `json:"trigger,omitempty"` // hint for model on when to activate

	// --- content ---
	Prompt string      `json:"prompt"`          // SKILL.md body (markdown)
	Files  []SkillFile `json:"files,omitempty"` // bundled files to deploy on activation
	Dir    string      `json:"-"`               // local directory path (not serialized)
}

// HasFiles reports whether this skill includes bundled files.
func (s *Skill) HasFiles() bool {
	return len(s.Files) > 0
}

// SandboxDir returns the root path where this skill is deployed in the sandbox.
// Uses /home/gem/skills/ which is writable in the sandbox environment.
func (s *Skill) SandboxDir() string {
	return "/home/gem/skills/" + s.Name
}

// IsUserInvocable reports whether users can invoke this skill via /name.
func (s *Skill) IsUserInvocable() bool {
	if s.UserInvocable == nil {
		return true // default: yes
	}
	return *s.UserInvocable
}

// IsModelInvocable reports whether the model can auto-activate this skill.
func (s *Skill) IsModelInvocable() bool {
	return !s.DisableModelInvocation
}

// ResolvePrompt returns the prompt with:
//   - $ARGUMENTS replaced (or appended if absent)
//   - relative script paths rewritten to sandbox absolute paths
func (s *Skill) ResolvePrompt(args string) string {
	prompt := s.Prompt

	// Rewrite relative paths to sandbox absolute paths so the model can
	// use commands from SKILL.md verbatim.
	if s.HasFiles() {
		prompt = strings.ReplaceAll(prompt, "scripts/", s.SandboxDir()+"/scripts/")
		prompt = strings.ReplaceAll(prompt, "references/", s.SandboxDir()+"/references/")
		prompt = strings.ReplaceAll(prompt, "assets/", s.SandboxDir()+"/assets/")
	}

	if args == "" {
		return prompt
	}
	if strings.Contains(prompt, "$ARGUMENTS") {
		return strings.ReplaceAll(prompt, "$ARGUMENTS", args)
	}
	return prompt + "\n\nARGUMENTS: " + args
}

// SkillRegistry holds all loaded skills and provides lookup.
type SkillRegistry struct {
	mu     sync.RWMutex
	skills map[string]*Skill
}

func NewSkillRegistry() *SkillRegistry {
	return &SkillRegistry{skills: make(map[string]*Skill)}
}

// LoadDir scans a directory for skill definitions. Each entry must be a
// subdirectory containing a SKILL.md file (Claude Code compatible layout).
func (r *SkillRegistry) LoadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no skills directory is fine
		}
		return fmt.Errorf("failed to read skills dir: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skill, err := loadSkillDir(filepath.Join(dir, e.Name()))
		if err != nil {
			return fmt.Errorf("failed to load skill %s: %w", e.Name(), err)
		}
		r.skills[skill.Name] = skill
	}
	return nil
}

// loadSkillDir loads a skill from a directory. It looks for SKILL.md as the
// entry file. All other files (recursively) are collected as bundled files.
func loadSkillDir(dir string) (*Skill, error) {
	entryPath := filepath.Join(dir, skillEntryFile)
	if _, err := os.Stat(entryPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("directory %s missing %s", dir, skillEntryFile)
	}

	skill, err := parseSkillFile(entryPath)
	if err != nil {
		return nil, err
	}

	// Name defaults to directory name
	dirName := filepath.Base(dir)
	if skill.Name == "" || skill.Name == "SKILL" {
		skill.Name = dirName
	}
	skill.Dir = dir

	// Walk the directory tree to collect all bundled files
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Skip the entry file itself
		if filepath.Base(path) == skillEntryFile && filepath.Dir(path) == dir {
			return nil
		}
		relPath, _ := filepath.Rel(dir, path)
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", relPath, err)
		}
		skill.Files = append(skill.Files, SkillFile{
			RelPath: relPath,
			Content: string(content),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk skill dir %s: %w", dir, err)
	}

	return skill, nil
}

// Get returns a skill by name, or nil if not found.
func (r *SkillRegistry) Get(name string) *Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.skills[name]
}

// List returns all loaded skills.
func (r *SkillRegistry) List() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*Skill, 0, len(r.skills))
	for _, s := range r.skills {
		out = append(out, s)
	}
	return out
}

// ListUserInvocable returns skills that users can trigger via /name.
func (r *SkillRegistry) ListUserInvocable() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []*Skill
	for _, s := range r.skills {
		if s.IsUserInvocable() {
			out = append(out, s)
		}
	}
	return out
}

// ListModelInvocable returns skills that the model can auto-activate.
func (r *SkillRegistry) ListModelInvocable() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []*Skill
	for _, s := range r.skills {
		if s.IsModelInvocable() {
			out = append(out, s)
		}
	}
	return out
}

// SkillSummary returns a compact listing of model-invocable skills suitable
// for injecting into a system prompt.
func (r *SkillRegistry) SkillSummary() string {
	skills := r.ListModelInvocable()
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n## 可用 Skills\n")
	sb.WriteString("通过 skill 工具 (action=activate) 激活后，你将获得该 skill 的专门工作流指导。\n\n")
	for _, s := range skills {
		fmt.Fprintf(&sb, "- **%s**: %s", s.Name, s.Description)
		if s.Trigger != "" {
			fmt.Fprintf(&sb, " (触发场景: %s)", s.Trigger)
		}
		if s.HasFiles() {
			fmt.Fprintf(&sb, " [含 %d 个文件]", len(s.Files))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// ActiveSkillsPrompt builds the prompt fragment for a set of active skill names.
// args is optional per-skill arguments (keyed by skill name).
func (r *SkillRegistry) ActiveSkillsPrompt(active []string, args map[string]string) string {
	if len(active) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, name := range active {
		s := r.Get(name)
		if s == nil {
			continue
		}

		prompt := s.ResolvePrompt(args[name])
		fmt.Fprintf(&sb, "\n\n---\n## [Active Skill: %s]\n%s", s.Name, prompt)

		if s.HasFiles() {
			sandboxDir := s.SandboxDir()
			fmt.Fprintf(&sb, "\n\n### 已部署文件 (%s)\n", sandboxDir)
			for _, f := range s.Files {
				fmt.Fprintf(&sb, "- `%s/%s`\n", sandboxDir, f.RelPath)
			}
		}
	}
	return sb.String()
}

// --- frontmatter parser (minimal, no external deps) ---

// parseSkillFile reads a .md file with --- delimited frontmatter.
// Compatible with Claude Code / agentskills.io frontmatter fields.
func parseSkillFile(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	content := string(data)

	// Default name from filename
	base := filepath.Base(path)
	defaultName := strings.TrimSuffix(base, filepath.Ext(base))

	skill := &Skill{Name: defaultName}

	// Split frontmatter
	if strings.HasPrefix(content, "---") {
		parts := strings.SplitN(content[3:], "---", 2)
		if len(parts) == 2 {
			parseFrontmatter(strings.TrimSpace(parts[0]), skill)
			skill.Prompt = strings.TrimSpace(parts[1])
		} else {
			skill.Prompt = strings.TrimSpace(content)
		}
	} else {
		skill.Prompt = strings.TrimSpace(content)
	}

	if skill.Name == "" {
		skill.Name = defaultName
	}

	return skill, nil
}

// parseFrontmatter does a simple line-by-line YAML-like parse.
// Supports both agentskills.io standard fields and Claude Code extensions.
func parseFrontmatter(block string, s *Skill) {
	var metadataMode bool
	for _, line := range strings.Split(block, "\n") {
		// Handle metadata block (indented key-value pairs)
		if metadataMode {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				metadataMode = false
				// Fall through to parse this line normally
			} else {
				key, val, ok := strings.Cut(trimmed, ":")
				if ok {
					if s.Metadata == nil {
						s.Metadata = make(map[string]string)
					}
					s.Metadata[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(val), `"'`)
				}
				continue
			}
		}

		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)

		switch key {
		// agentskills.io standard
		case "name":
			s.Name = val
		case "description":
			s.Description = val
		case "license":
			s.License = val
		case "compatibility":
			s.Compatibility = val
		case "metadata":
			metadataMode = true

		// Claude Code extensions
		case "disable-model-invocation":
			s.DisableModelInvocation = val == "true"
		case "user-invocable":
			b := val != "false"
			s.UserInvocable = &b
		case "tools":
			s.Tools = val
		case "context":
			s.Context = val

		// Our extensions
		case "trigger":
			s.Trigger = val
		}
	}
}
