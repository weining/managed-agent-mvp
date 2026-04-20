package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func newTestMemoryStore(t *testing.T) *FileMemoryStore {
	t.Helper()
	dir := t.TempDir()
	store, err := NewFileMemoryStore(filepath.Join(dir, "memory.json"))
	if err != nil {
		t.Fatalf("NewFileMemoryStore: %v", err)
	}
	return store
}

func TestFileMemoryStore_SaveAndGet(t *testing.T) {
	store := newTestMemoryStore(t)

	entry := MemoryEntry{
		Key:     "user_pref_lang",
		Content: "用户偏好中文",
		Tags:    []string{"preference"},
		Source:  "sess123",
	}
	if err := store.Save(entry); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Get("user_pref_lang")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.Content != "用户偏好中文" {
		t.Errorf("Content: got %q, want %q", got.Content, "用户偏好中文")
	}
	if got.ID == "" {
		t.Error("ID should be auto-generated")
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
}

func TestFileMemoryStore_GetNotFound(t *testing.T) {
	store := newTestMemoryStore(t)
	got, err := store.Get("nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent key")
	}
}

func TestFileMemoryStore_SaveUpdatesExisting(t *testing.T) {
	store := newTestMemoryStore(t)
	store.Save(MemoryEntry{Key: "k1", Content: "v1", Source: "s1"})
	store.Save(MemoryEntry{Key: "k1", Content: "v2", Source: "s2"})
	got, _ := store.Get("k1")
	if got.Content != "v2" {
		t.Errorf("Content: got %q, want v2", got.Content)
	}
	entries, _ := store.List(nil, 100)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry after upsert, got %d", len(entries))
	}
}

func TestFileMemoryStore_SaveDeduplicatesByContent(t *testing.T) {
	store := newTestMemoryStore(t)
	store.Save(MemoryEntry{Key: "user_profession", Content: "用户是服务端程序员", Tags: []string{"pref"}, Source: "s1"})
	store.Save(MemoryEntry{Key: "user_occupation", Content: "用户是服务端程序员", Tags: []string{"work"}, Source: "s2"})

	entries, _ := store.List(nil, 100)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after content dedup, got %d", len(entries))
	}
	// Should adopt the newer key
	if entries[0].Key != "user_occupation" {
		t.Errorf("Key: got %q, want user_occupation", entries[0].Key)
	}
	// Tags should be merged
	if len(entries[0].Tags) != 2 {
		t.Errorf("Tags: got %v, want [pref work]", entries[0].Tags)
	}
}

func TestFileMemoryStore_SaveDeduplicatesNormalized(t *testing.T) {
	store := newTestMemoryStore(t)
	store.Save(MemoryEntry{Key: "k1", Content: "Hello World"})
	store.Save(MemoryEntry{Key: "k2", Content: "  hello world  "}) // same after normalization

	entries, _ := store.List(nil, 100)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after normalized content dedup, got %d", len(entries))
	}
}

func TestFileMemoryStore_Delete(t *testing.T) {
	store := newTestMemoryStore(t)
	store.Save(MemoryEntry{Key: "k1", Content: "v1"})
	store.Save(MemoryEntry{Key: "k2", Content: "v2"})
	if err := store.Delete("k1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ := store.Get("k1")
	if got != nil {
		t.Error("expected nil after delete")
	}
	got, _ = store.Get("k2")
	if got == nil {
		t.Error("k2 should still exist")
	}
}

func TestFileMemoryStore_DeleteNotFound(t *testing.T) {
	store := newTestMemoryStore(t)
	err := store.Delete("nonexistent")
	if err != nil {
		t.Fatalf("Delete nonexistent should not error, got: %v", err)
	}
}

func TestFileMemoryStore_List(t *testing.T) {
	store := newTestMemoryStore(t)
	store.Save(MemoryEntry{Key: "k1", Content: "v1", Tags: []string{"pref"}})
	store.Save(MemoryEntry{Key: "k2", Content: "v2", Tags: []string{"project"}})
	store.Save(MemoryEntry{Key: "k3", Content: "v3", Tags: []string{"pref", "project"}})
	all, _ := store.List(nil, 100)
	if len(all) != 3 {
		t.Errorf("List all: got %d, want 3", len(all))
	}
	prefs, _ := store.List([]string{"pref"}, 100)
	if len(prefs) != 2 {
		t.Errorf("List pref: got %d, want 2", len(prefs))
	}
	limited, _ := store.List(nil, 2)
	if len(limited) != 2 {
		t.Errorf("List limit=2: got %d, want 2", len(limited))
	}
}

func TestFileMemoryStore_Search(t *testing.T) {
	store := newTestMemoryStore(t)
	store.Save(MemoryEntry{Key: "k1", Content: "用户喜欢 Go 语言", Tags: []string{"pref"}})
	store.Save(MemoryEntry{Key: "k2", Content: "项目使用 Python", Tags: []string{"project"}})
	store.Save(MemoryEntry{Key: "k3", Content: "Go 模块结构", Tags: []string{"project"}})
	results, _ := store.Search("Go", nil, 10)
	if len(results) != 2 {
		t.Errorf("Search 'Go': got %d, want 2", len(results))
	}
	results, _ = store.Search("Go", []string{"project"}, 10)
	if len(results) != 1 {
		t.Errorf("Search 'Go' tag=project: got %d, want 1", len(results))
	}
	results, _ = store.Search("Go", nil, 1)
	if len(results) != 1 {
		t.Errorf("Search limit=1: got %d, want 1", len(results))
	}
}

func TestBuildMemoryPrompt_Empty(t *testing.T) {
	result := buildMemoryPrompt(nil)
	if result != "" {
		t.Errorf("expected empty string for nil entries, got %q", result)
	}
	result = buildMemoryPrompt([]MemoryEntry{})
	if result != "" {
		t.Errorf("expected empty string for empty entries, got %q", result)
	}
}

func TestBuildMemoryPrompt_WithEntries(t *testing.T) {
	entries := []MemoryEntry{
		{Key: "k1", Content: "value1", Tags: []string{"pref"}},
		{Key: "k2", Content: "value2", Tags: []string{"project"}},
	}
	result := buildMemoryPrompt(entries)
	if !strings.Contains(result, "跨会话记忆") {
		t.Error("expected header in prompt")
	}
	if !strings.Contains(result, "k1") || !strings.Contains(result, "value1") {
		t.Error("expected k1/value1 in prompt")
	}
	if !strings.Contains(result, "k2") || !strings.Contains(result, "value2") {
		t.Error("expected k2/value2 in prompt")
	}
}

func TestExecuteMemoryTool_SaveAndRecall(t *testing.T) {
	store := newTestMemoryStore(t)

	// Save
	output, isError := executeMemoryTool(store, "sess1", map[string]any{
		"action":  "save",
		"key":     "test_key",
		"content": "test value",
		"tags":    []any{"test"},
	})
	if isError {
		t.Fatalf("save returned error: %s", output)
	}
	if !strings.Contains(output, "Memory saved") {
		t.Errorf("unexpected save output: %s", output)
	}

	// Recall
	output, isError = executeMemoryTool(store, "sess1", map[string]any{
		"action": "recall",
		"query":  "test",
	})
	if isError {
		t.Fatalf("recall returned error: %s", output)
	}
	if !strings.Contains(output, "test_key") {
		t.Errorf("recall should contain key: %s", output)
	}

	// List
	output, isError = executeMemoryTool(store, "sess1", map[string]any{
		"action": "list",
	})
	if isError {
		t.Fatalf("list returned error: %s", output)
	}
	if !strings.Contains(output, "test_key") {
		t.Errorf("list should contain key: %s", output)
	}

	// Delete
	output, isError = executeMemoryTool(store, "sess1", map[string]any{
		"action": "delete",
		"key":    "test_key",
	})
	if isError {
		t.Fatalf("delete returned error: %s", output)
	}

	// Verify deleted
	output, isError = executeMemoryTool(store, "sess1", map[string]any{
		"action": "list",
	})
	if strings.Contains(output, "test_key") {
		t.Error("key should be deleted")
	}
}

func TestExecuteMemoryTool_InvalidAction(t *testing.T) {
	store := newTestMemoryStore(t)
	_, isError := executeMemoryTool(store, "sess1", map[string]any{
		"action": "invalid",
	})
	if !isError {
		t.Error("expected error for invalid action")
	}
}

func TestExecuteMemoryTool_SaveMissingFields(t *testing.T) {
	store := newTestMemoryStore(t)
	_, isError := executeMemoryTool(store, "sess1", map[string]any{
		"action": "save",
	})
	if !isError {
		t.Error("expected error when key/content missing")
	}
}
