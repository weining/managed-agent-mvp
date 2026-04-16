package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

const uploadMaxSize = 100 << 20 // 100 MB

type httpSSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func (h *httpSSEWriter) WriteEvent(event, data string) {
	fmt.Fprintf(h.w, "event: %s\ndata: %s\n\n", event, data)
}

func (h *httpSSEWriter) Flush() {
	h.flusher.Flush()
}

func setupRoutes(deps *AgentDeps) http.Handler {
	mux := http.NewServeMux()

	store := deps.Store

	// Serve frontend
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, "frontend/index.html")
	})

	// Create session
	mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		sess, err := store.Create()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, sess)
	})

	// List sessions
	mux.HandleFunc("GET /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		sessions, err := store.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, sessions)
	})

	// Get session detail
	mux.HandleFunc("GET /api/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		sess, err := store.Get(id)
		if err != nil {
			http.Error(w, "Session not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, sess)
	})

	// Send message (SSE streaming response)
	mux.HandleFunc("POST /api/sessions/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		sess, err := store.Get(id)
		if err != nil {
			http.Error(w, "Session not found", http.StatusNotFound)
			return
		}

		var body struct {
			Message     string       `json:"message"`
			Attachments []Attachment `json:"attachments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		body.Message = strings.TrimSpace(body.Message)
		if body.Message == "" && len(body.Attachments) == 0 {
			http.Error(w, "Message or attachments are required", http.StatusBadRequest)
			return
		}

		// Handle /skill-name [args] as explicit skill activation
		msg := body.Message
		if after, ok := strings.CutPrefix(msg, "/"); ok {
			// Split into skill name and optional arguments
			skillName, skillArgs, _ := strings.Cut(after, " ")
			skillArgs = strings.TrimSpace(skillArgs)

			if skill := deps.Skills.Get(skillName); skill != nil && skill.IsUserInvocable() {
				// Activate the skill on this session
				sess, _ = store.Get(id) // reload
				alreadyActive := false
				for _, s := range sess.ActiveSkills {
					if s == skillName {
						alreadyActive = true
						break
					}
				}
				if !alreadyActive {
					store.SetActiveSkills(id, append(sess.ActiveSkills, skillName))
				}
				// Store arguments if provided
				if skillArgs != "" {
					store.SetSkillArgs(id, skillName, skillArgs)
				}

				// Return a confirmation via SSE without invoking Claude
				flusher, ok := w.(http.Flusher)
				if !ok {
					http.Error(w, "Streaming not supported", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				sse := &httpSSEWriter{w: w, flusher: flusher}

				status := fmt.Sprintf("Skill **%s** activated.", skillName)
				if alreadyActive {
					status = fmt.Sprintf("Skill **%s** is already active.", skillName)
				}

				// Deploy files to sandbox if this is a fresh activation
				if !alreadyActive && skill.HasFiles() {
					deployed, err := deploySkillFiles(deps.Sandbox, skill)
					if err != nil {
						status += fmt.Sprintf("\nWarning: file deployment failed: %v", err)
					} else {
						status += fmt.Sprintf("\n%d file(s) deployed to %s: %s",
							len(deployed), skill.SandboxDir(), strings.Join(deployed, ", "))
					}
				}

				jsonData, _ := json.Marshal(map[string]string{"content": status})
				sse.WriteEvent("text", string(jsonData))
				sse.Flush()

				// Emit as a system note so it shows in history
				store.EmitEvent(id, Event{Type: "user_message", Content: msg})
				store.EmitEvent(id, Event{Type: "assistant_message", Content: status})

				sse.WriteEvent("done", "{}")
				sse.Flush()
				return
			}
			// Not a known skill — fall through to normal agent processing
		}

		// Setup SSE
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		sse := &httpSSEWriter{w: w, flusher: flusher}

		if err := RunAgentWithContent(deps, sess, UserMessageContent{
			Text:        body.Message,
			Attachments: body.Attachments,
		}, sse); err != nil {
			log.Printf("Agent error for session %s: %v", id, err)
		}
	})

	// CORS preflight
	mux.HandleFunc("OPTIONS /api/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
		w.WriteHeader(http.StatusNoContent)
	})

	// Upload file into sandbox for a session
	// Form fields: file (required), path (optional dest), message (optional — if set, triggers agent via SSE)
	mux.HandleFunc("POST /api/sessions/{id}/upload", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		sess, err := store.Get(id)
		if err != nil {
			http.Error(w, "Session not found", http.StatusNotFound)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, uploadMaxSize)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "Failed to parse form (max 100 MB): "+err.Error(), http.StatusBadRequest)
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "Missing 'file' field in form: "+err.Error(), http.StatusBadRequest)
			return
		}
		defer file.Close()

		// Optional dest path; default to /home/gem/uploads/<filename>
		destPath := strings.TrimSpace(r.FormValue("path"))
		if destPath == "" {
			destPath = "/home/gem/uploads/" + header.Filename
		}

		sandboxPath, err := deps.Sandbox.UploadFile(file, destPath)
		if err != nil {
			log.Printf("Upload error for session %s: %v", id, err)
			http.Error(w, "Upload failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// If no message, return JSON result
		userMsg := strings.TrimSpace(r.FormValue("message"))
		if userMsg == "" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			mimeType := detectMIMEType(header.Filename, header.Header.Get("Content-Type"))
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"path":      sandboxPath,
				"filename":  header.Filename,
				"size":      header.Size,
				"mime_type": mimeType,
				"is_image":  strings.HasPrefix(mimeType, "image/"),
			})
			return
		}

		mimeType := detectMIMEType(header.Filename, header.Header.Get("Content-Type"))

		// Stream agent response via SSE
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		sse := &httpSSEWriter{w: w, flusher: flusher}
		if err := RunAgentWithContent(deps, sess, UserMessageContent{
			Text: userMsg,
			Attachments: []Attachment{{
				Path:     sandboxPath,
				Filename: header.Filename,
				Size:     header.Size,
				MIMEType: mimeType,
				IsImage:  strings.HasPrefix(mimeType, "image/"),
			}},
		}, sse); err != nil {
			log.Printf("Agent error for upload session %s: %v", id, err)
		}
	})

	// Stream file content from sandbox for inline previews.
	mux.HandleFunc("GET /api/files/content", func(w http.ResponseWriter, r *http.Request) {
		sandboxPath := r.URL.Query().Get("path")
		if sandboxPath == "" {
			http.Error(w, "path query parameter is required", http.StatusBadRequest)
			return
		}

		reader, err := deps.Sandbox.DownloadFile(sandboxPath)
		if err != nil {
			log.Printf("Content fetch error for %s: %v", sandboxPath, err)
			http.Error(w, "Failed to fetch file: "+err.Error(), http.StatusInternalServerError)
			return
		}

		contentType := detectMIMEType(filepath.Base(sandboxPath), "")
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "private, max-age=300")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		if _, err := io.Copy(w, reader); err != nil {
			log.Printf("Error streaming preview %s: %v", sandboxPath, err)
		}
	})

	// Download file from sandbox
	mux.HandleFunc("GET /api/files/download", func(w http.ResponseWriter, r *http.Request) {
		sandboxPath := r.URL.Query().Get("path")
		if sandboxPath == "" {
			http.Error(w, "path query parameter is required", http.StatusBadRequest)
			return
		}

		reader, err := deps.Sandbox.DownloadFile(sandboxPath)
		if err != nil {
			log.Printf("Download error for %s: %v", sandboxPath, err)
			http.Error(w, "Failed to download file: "+err.Error(), http.StatusInternalServerError)
			return
		}

		filename := filepath.Base(sandboxPath)
		encodedFilename := url.PathEscape(filename)
		w.Header().Set("Content-Disposition",
			fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, encodedFilename, encodedFilename))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		if _, err := io.Copy(w, reader); err != nil {
			log.Printf("Error streaming file %s: %v", sandboxPath, err)
		}
	})

	return mux
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func detectMIMEType(filename, headerType string) string {
	if headerType != "" {
		if mediaType, _, err := mime.ParseMediaType(headerType); err == nil {
			return mediaType
		}
		return headerType
	}
	if byExt := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))); byExt != "" {
		if mediaType, _, err := mime.ParseMediaType(byExt); err == nil {
			return mediaType
		}
		return byExt
	}
	return ""
}
