package main

import "sync"

// ImageCache holds base64-encoded image data keyed by sandbox path.
// It is safe for concurrent use and lives for the process lifetime.
type ImageCache struct {
	mu    sync.RWMutex
	items map[string]cachedImage
}

type cachedImage struct {
	MIMEType string
	Base64   string
}

// NewImageCache returns an empty ImageCache.
func NewImageCache() *ImageCache {
	return &ImageCache{items: make(map[string]cachedImage)}
}

// Get returns the cached image for path, if present.
func (c *ImageCache) Get(path string) (cachedImage, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	item, ok := c.items[path]
	return item, ok
}

// Set stores the image data for path, overwriting any existing entry.
func (c *ImageCache) Set(path, mimeType, base64Data string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[path] = cachedImage{MIMEType: mimeType, Base64: base64Data}
}
