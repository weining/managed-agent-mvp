package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildOAIMessages_ImageBlock(t *testing.T) {
	msgs := []ClaudeMessage{
		{
			Role: "user",
			Content: []ContentBlock{
				{Type: "text", Text: "describe this"},
				{Type: "image", ImageMIMEType: "image/png", ImageData: "abc=="},
			},
		},
	}

	oaiMsgs := buildOAIMessages("", msgs)

	var userMsg *oaiMessage
	for i := range oaiMsgs {
		if oaiMsgs[i].Role == "user" {
			userMsg = &oaiMsgs[i]
			break
		}
	}
	if userMsg == nil {
		t.Fatal("no user message found")
	}

	parts, ok := userMsg.Content.([]interface{})
	if !ok {
		t.Fatalf("expected array content, got %T: %v", userMsg.Content, userMsg.Content)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}

	textPart := parts[0].(map[string]interface{})
	if textPart["type"] != "text" {
		t.Errorf("part[0].type: got %v, want text", textPart["type"])
	}

	imgPart := parts[1].(map[string]interface{})
	if imgPart["type"] != "image_url" {
		t.Errorf("part[1].type: got %v, want image_url", imgPart["type"])
	}
	imgURL := imgPart["image_url"].(map[string]interface{})
	url, _ := imgURL["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Errorf("image_url.url: got %q, expected data:image/png;base64, prefix", url)
	}
	if !strings.HasSuffix(url, "abc==") {
		t.Errorf("image_url.url: got %q, expected abc== suffix", url)
	}
}

func TestBuildOAIMessages_TextOnly(t *testing.T) {
	msgs := []ClaudeMessage{
		{
			Role:    "user",
			Content: []ContentBlock{{Type: "text", Text: "hello"}},
		},
	}
	oaiMsgs := buildOAIMessages("", msgs)
	for _, m := range oaiMsgs {
		if m.Role == "user" {
			if _, ok := m.Content.(string); !ok {
				t.Errorf("expected string content for text-only, got %T", m.Content)
			}
		}
	}
}

func TestBuildOAIMessages_JSONRoundtrip(t *testing.T) {
	msgs := []ClaudeMessage{
		{
			Role: "user",
			Content: []ContentBlock{
				{Type: "image", ImageMIMEType: "image/jpeg", ImageData: "data"},
			},
		},
	}
	oaiMsgs := buildOAIMessages("", msgs)
	_, err := json.Marshal(oaiMsgs)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
}
