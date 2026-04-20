package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"managed-agent/llm"
)

// MemoryEntry represents a single memory item.
type MemoryEntry struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Content   string    `json:"content"`
	Tags      []string  `json:"tags,omitempty"`
	Source    string    `json:"source,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MemoryStore is the interface for persistent memory storage.
type MemoryStore interface {
	Save(entry MemoryEntry) error
	Get(key string) (*MemoryEntry, error)
	Search(query string, tags []string, limit int) ([]MemoryEntry, error)
	Delete(key string) error
	List(tags []string, limit int) ([]MemoryEntry, error)
}

// FileMemoryStore implements MemoryStore using a JSON file.
type FileMemoryStore struct {
	mu   sync.RWMutex
	path string
}

// NewFileMemoryStore creates a FileMemoryStore. Creates parent dir and empty file if needed.
func NewFileMemoryStore(path string) (*FileMemoryStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create memory dir: %w", err)
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(path, []byte("[]"), 0o644); err != nil {
			return nil, fmt.Errorf("failed to create memory file: %w", err)
		}
	}
	return &FileMemoryStore{path: path}, nil
}

func (f *FileMemoryStore) load() ([]MemoryEntry, error) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return nil, fmt.Errorf("failed to read memory file: %w", err)
	}
	var entries []MemoryEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("failed to unmarshal memory: %w", err)
	}
	return entries, nil
}

func (f *FileMemoryStore) save(entries []MemoryEntry) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal memory: %w", err)
	}
	return os.WriteFile(f.path, data, 0o644)
}

// Save stores or updates a memory entry. Deduplication strategy:
//  1. If an entry with the same key exists, update it in place.
//  2. If an entry with identical content exists (different key), merge: update
//     the existing entry's key to the new key and refresh metadata.
//  3. Otherwise create a new entry.
func (f *FileMemoryStore) Save(entry MemoryEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	entries, err := f.load()
	if err != nil {
		return err
	}

	now := time.Now()

	// 1. Exact key match — update in place
	for i, e := range entries {
		if e.Key == entry.Key {
			entries[i].Content = entry.Content
			entries[i].Tags = entry.Tags
			entries[i].Source = entry.Source
			entries[i].UpdatedAt = now
			return f.save(entries)
		}
	}

	// 2. Content dedup — if identical content already stored under a different key, merge
	normalizedNew := normalizeContent(entry.Content)
	for i, e := range entries {
		if normalizeContent(e.Content) == normalizedNew {
			entries[i].Key = entry.Key // adopt the newer key
			entries[i].Tags = mergeTags(entries[i].Tags, entry.Tags)
			entries[i].Source = entry.Source
			entries[i].UpdatedAt = now
			return f.save(entries)
		}
	}

	// 3. New entry
	if entry.ID == "" {
		entry.ID = generateID()
	}
	entry.CreatedAt = now
	entry.UpdatedAt = now
	entries = append(entries, entry)
	return f.save(entries)
}

// normalizeContent trims whitespace and lowercases for content comparison.
func normalizeContent(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// mergeTags combines two tag slices, deduplicating.
func mergeTags(existing, incoming []string) []string {
	seen := make(map[string]struct{}, len(existing))
	for _, t := range existing {
		seen[t] = struct{}{}
	}
	merged := append([]string{}, existing...)
	for _, t := range incoming {
		if _, ok := seen[t]; !ok {
			merged = append(merged, t)
			seen[t] = struct{}{}
		}
	}
	return merged
}

// Get retrieves a memory entry by key. Returns nil, nil if not found.
func (f *FileMemoryStore) Get(key string) (*MemoryEntry, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	entries, err := f.load()
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Key == key {
			return &e, nil
		}
	}
	return nil, nil
}

func (f *FileMemoryStore) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, err := f.load()
	if err != nil {
		return err
	}
	filtered := entries[:0]
	for _, e := range entries {
		if e.Key != key {
			filtered = append(filtered, e)
		}
	}
	return f.save(filtered)
}

func (f *FileMemoryStore) List(tags []string, limit int) ([]MemoryEntry, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	entries, err := f.load()
	if err != nil {
		return nil, err
	}
	var result []MemoryEntry
	for _, e := range entries {
		if len(tags) > 0 && !hasAnyTag(e.Tags, tags) {
			continue
		}
		result = append(result, e)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (f *FileMemoryStore) Search(query string, tags []string, limit int) ([]MemoryEntry, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	entries, err := f.load()
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(query)
	var result []MemoryEntry
	for _, e := range entries {
		if len(tags) > 0 && !hasAnyTag(e.Tags, tags) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.Content), q) &&
			!strings.Contains(strings.ToLower(e.Key), q) {
			continue
		}
		result = append(result, e)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

func hasAnyTag(entryTags, filterTags []string) bool {
	for _, ft := range filterTags {
		for _, et := range entryTags {
			if et == ft {
				return true
			}
		}
	}
	return false
}

// buildMemoryPrompt formats memory entries into a system prompt section.
func buildMemoryPrompt(entries []MemoryEntry) string {
	if len(entries) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n## 跨会话记忆\n\n以下是从之前会话中积累的记忆，请在回答时参考：\n\n")

	totalLen := 0
	const maxChars = 6000 // ~2000 tokens rough estimate
	for _, e := range entries {
		line := fmt.Sprintf("- **%s**: %s", e.Key, e.Content)
		if len(e.Tags) > 0 {
			line += " [" + strings.Join(e.Tags, ", ") + "]"
		}
		line += "\n"
		if totalLen+len(line) > maxChars {
			break
		}
		sb.WriteString(line)
		totalLen += len(line)
	}
	return sb.String()
}

// eventsToText converts events to a text summary for LLM consumption.
func eventsToText(events []Event) string {
	var sb strings.Builder
	for _, evt := range events {
		switch evt.Type {
		case "user_message":
			text := extractUserMessageText(evt.Content)
			if text != "" {
				fmt.Fprintf(&sb, "User: %s\n", text)
			}
		case "assistant_message":
			text, _ := evt.Content.(string)
			if text != "" {
				fmt.Fprintf(&sb, "Assistant: %s\n", text)
			}
		case "tool_use":
			m := toStringMap(evt.Content)
			name, _ := m["name"].(string)
			fmt.Fprintf(&sb, "Tool call: %s\n", name)
		}
	}
	return sb.String()
}

const memoryExtractionPrompt = `分析以下对话片段，提取值得在未来会话中记住的信息。只提取以下类型：
- 用户明确的偏好和习惯
- 项目相关的关键决策和约定
- 重要的技术上下文（架构选择、命名规范等）
- 用户纠正过的错误理解

如果没有值得记忆的内容，返回空数组。
输出严格 JSON 格式：[{"key": "语义键名", "content": "记忆内容", "tags": ["分类"]}]
不要输出任何其他内容，只输出 JSON。`

// extractMemories uses LLM to extract memorable facts from recent events.
func extractMemories(sess *Session, store *SessionStore, memStore MemoryStore, llmClient llm.LLMClient) {
	events := sess.Events
	startIdx := sess.MemoryExtractedIndex
	if startIdx >= len(events) {
		return
	}
	newEvents := events[startIdx:]
	if len(newEvents) < 4 {
		return
	}

	text := eventsToText(newEvents)
	if strings.TrimSpace(text) == "" {
		return
	}

	// Truncate to avoid sending too much text
	const maxChars = 8000
	if len(text) > maxChars {
		text = text[:maxChars]
	}

	messages := []llm.ClaudeMessage{
		{
			Role: "user",
			Content: []llm.ContentBlock{
				{Type: "text", Text: text},
			},
		},
	}

	resp, err := llmClient.CallStream(memoryExtractionPrompt, messages, nil, func(string, any) {})
	if err != nil {
		log.Printf("memory extraction LLM call failed: %v", err)
		return
	}

	var respText string
	for _, block := range resp.Content {
		if block.Type == "text" {
			respText += block.Text
		}
	}

	var extracted []struct {
		Key     string   `json:"key"`
		Content string   `json:"content"`
		Tags    []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(respText)), &extracted); err != nil {
		log.Printf("memory extraction parse failed: %v (response: %s)", err, respText)
		return
	}

	for _, item := range extracted {
		if item.Key == "" || item.Content == "" {
			continue
		}
		memStore.Save(MemoryEntry{
			Key:     item.Key,
			Content: item.Content,
			Tags:    item.Tags,
			Source:  sess.ID,
		})
	}

	// Update session to mark extraction progress
	store.UpdateMemoryFields(sess.ID, len(events), sess.ConversationSummary, sess.SummaryUpToEventIndex)
	log.Printf("memory extraction: extracted %d memories from %d new events", len(extracted), len(newEvents))
}

const summarizationPrompt = `将以下对话历史压缩为简洁摘要，保留关键信息：用户目标、已完成操作、重要决策、未解决问题。
用中文输出纯文本摘要，不要使用 JSON 格式。保持在 500 字以内。`

const incrementalSummarizationPrompt = `以下是之前的对话摘要，以及摘要之后新发生的对话。请将两者合并为一份更新的摘要，保留关键信息：用户目标、已完成操作、重要决策、未解决问题。
用中文输出纯文本摘要，不要使用 JSON 格式。保持在 500 字以内。`

// summarizeEvents uses LLM to create or incrementally update a conversation summary.
// If prevSummary is non-empty, only the new events (after the previous summary) are
// sent along with the old summary, avoiding re-processing the entire history.
func summarizeEvents(events []Event, prevSummary string, llmClient llm.LLMClient) (string, error) {
	text := eventsToText(events)
	if strings.TrimSpace(text) == "" {
		return prevSummary, nil
	}

	var prompt string
	var inputText string

	if prevSummary != "" {
		// Incremental: old summary + new events only
		prompt = incrementalSummarizationPrompt
		inputText = fmt.Sprintf("## 之前的摘要\n\n%s\n\n## 新增对话\n\n%s", prevSummary, text)
	} else {
		// Full: first-time summarization
		prompt = summarizationPrompt
		inputText = text
	}

	const maxChars = 12000
	if len(inputText) > maxChars {
		inputText = inputText[:maxChars]
	}

	messages := []llm.ClaudeMessage{
		{
			Role: "user",
			Content: []llm.ContentBlock{
				{Type: "text", Text: inputText},
			},
		},
	}

	resp, err := llmClient.CallStream(prompt, messages, nil, func(string, any) {})
	if err != nil {
		return "", fmt.Errorf("summarization LLM call failed: %w", err)
	}

	var sb strings.Builder
	for _, block := range resp.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	return strings.TrimSpace(sb.String()), nil
}
