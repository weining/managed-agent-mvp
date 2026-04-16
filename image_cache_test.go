package main

import "testing"

func TestImageCache_GetSet(t *testing.T) {
	c := NewImageCache()

	// Get on empty cache
	if _, ok := c.Get("/some/path.jpg"); ok {
		t.Error("expected cache miss on empty cache")
	}

	// Set and Get
	c.Set("/some/path.jpg", "image/jpeg", "base64data==")
	item, ok := c.Get("/some/path.jpg")
	if !ok {
		t.Fatal("expected cache hit after Set")
	}
	if item.MIMEType != "image/jpeg" {
		t.Errorf("MIMEType: got %q, want image/jpeg", item.MIMEType)
	}
	if item.Base64 != "base64data==" {
		t.Errorf("Base64: got %q, want base64data==", item.Base64)
	}

	// Different path is still a miss
	if _, ok := c.Get("/other/path.png"); ok {
		t.Error("expected cache miss for different path")
	}
}

func TestImageCache_Overwrite(t *testing.T) {
	c := NewImageCache()
	c.Set("/img.jpg", "image/jpeg", "first")
	c.Set("/img.jpg", "image/jpeg", "second")
	item, _ := c.Get("/img.jpg")
	if item.Base64 != "second" {
		t.Errorf("expected overwrite: got %q, want second", item.Base64)
	}
}
