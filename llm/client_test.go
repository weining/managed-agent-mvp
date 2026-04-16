package llm

import (
	"encoding/json"
	"testing"
)

func TestContentBlockMarshalJSON_Image(t *testing.T) {
	b := ContentBlock{
		Type:          "image",
		ImageMIMEType: "image/jpeg",
		ImageData:     "abc123==",
	}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if out["type"] != "image" {
		t.Errorf("type: got %v, want image", out["type"])
	}
	src, ok := out["source"].(map[string]interface{})
	if !ok {
		t.Fatalf("source field missing or wrong type: %v", out)
	}
	if src["type"] != "base64" {
		t.Errorf("source.type: got %v, want base64", src["type"])
	}
	if src["media_type"] != "image/jpeg" {
		t.Errorf("source.media_type: got %v, want image/jpeg", src["media_type"])
	}
	if src["data"] != "abc123==" {
		t.Errorf("source.data: got %v, want abc123==", src["data"])
	}
	if _, exists := out["image_mime_type"]; exists {
		t.Error("image_mime_type should not appear at top level for image blocks")
	}
}

func TestContentBlockMarshalJSON_Text(t *testing.T) {
	b := ContentBlock{Type: "text", Text: "hello"}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var out map[string]interface{}
	json.Unmarshal(data, &out)
	if out["type"] != "text" {
		t.Errorf("type: got %v, want text", out["type"])
	}
	if out["text"] != "hello" {
		t.Errorf("text: got %v, want hello", out["text"])
	}
	if _, exists := out["source"]; exists {
		t.Error("source should not appear in text blocks")
	}
}

func TestContentBlockMarshalJSON_ToolResult(t *testing.T) {
	b := ContentBlock{
		Type:      "tool_result",
		ToolUseID: "tu_123",
		Content:   "result content",
		IsError:   false,
	}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var out map[string]interface{}
	json.Unmarshal(data, &out)
	if out["type"] != "tool_result" {
		t.Errorf("type: got %v, want tool_result", out["type"])
	}
	if out["tool_use_id"] != "tu_123" {
		t.Errorf("tool_use_id: got %v, want tu_123", out["tool_use_id"])
	}
}
