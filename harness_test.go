package main

import (
	"testing"

	"managed-agent/llm"
)

func TestBuildMessages_ImageBlock(t *testing.T) {
	cache := NewImageCache()
	cache.Set("/home/gem/uploads/photo.jpg", "image/jpeg", "base64abc==")

	events := []Event{
		{
			Type: "user_message",
			Content: map[string]interface{}{
				"text": "what is this?",
				"attachments": []interface{}{
					map[string]interface{}{
						"path":      "/home/gem/uploads/photo.jpg",
						"filename":  "photo.jpg",
						"mime_type": "image/jpeg",
						"is_image":  true,
					},
				},
			},
		},
	}

	msgs := buildMessages(events, cache)

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	msg := msgs[0]
	if msg.Role != "user" {
		t.Errorf("role: got %q, want user", msg.Role)
	}

	var textBlocks, imageBlocks []llm.ContentBlock
	for _, b := range msg.Content {
		switch b.Type {
		case "text":
			textBlocks = append(textBlocks, b)
		case "image":
			imageBlocks = append(imageBlocks, b)
		}
	}

	if len(textBlocks) != 1 {
		t.Errorf("expected 1 text block, got %d", len(textBlocks))
	} else if textBlocks[0].Text != "what is this?" {
		t.Errorf("text: got %q, want 'what is this?'", textBlocks[0].Text)
	}

	if len(imageBlocks) != 1 {
		t.Errorf("expected 1 image block, got %d", len(imageBlocks))
	} else {
		if imageBlocks[0].ImageMIMEType != "image/jpeg" {
			t.Errorf("ImageMIMEType: got %q, want image/jpeg", imageBlocks[0].ImageMIMEType)
		}
		if imageBlocks[0].ImageData != "base64abc==" {
			t.Errorf("ImageData: got %q, want base64abc==", imageBlocks[0].ImageData)
		}
	}
}

func TestBuildMessages_ImageCacheMiss_FallbackToText(t *testing.T) {
	cache := NewImageCache() // empty — cache miss

	events := []Event{
		{
			Type: "user_message",
			Content: map[string]interface{}{
				"text": "hello",
				"attachments": []interface{}{
					map[string]interface{}{
						"path":      "/home/gem/uploads/photo.jpg",
						"filename":  "photo.jpg",
						"mime_type": "image/jpeg",
						"is_image":  true,
					},
				},
			},
		},
	}

	msgs := buildMessages(events, cache)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	for _, b := range msgs[0].Content {
		if b.Type == "image" {
			t.Error("expected no image blocks on cache miss, got one")
		}
	}
}

func TestBuildMessages_TextOnly(t *testing.T) {
	cache := NewImageCache()
	events := []Event{
		{Type: "user_message", Content: "plain text message"},
	}
	msgs := buildMessages(events, cache)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Content[0].Text != "plain text message" {
		t.Errorf("text: got %q, want 'plain text message'", msgs[0].Content[0].Text)
	}
}
