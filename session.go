package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"crypto/rand"
	"encoding/hex"
)

type Event struct {
	ID        string      `json:"id"`
	Type      string      `json:"type"` // user_message | assistant_message | tool_use | tool_result | error
	Content   interface{} `json:"content"`
	Timestamp time.Time   `json:"timestamp"`
}

type Attachment struct {
	Path     string `json:"path"`
	Filename string `json:"filename"`
	Size     int64  `json:"size,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	IsImage  bool   `json:"is_image,omitempty"`
}

type UserMessageContent struct {
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

type Session struct {
	ID           string            `json:"id"`
	Events       []Event           `json:"events"`
	ActiveSkills []string          `json:"active_skills,omitempty"`
	SkillArgs    map[string]string `json:"skill_args,omitempty"` // per-skill arguments
	SandboxID    string            `json:"sandbox_id"`
	Token        string            `json:"token"`
	CreatedAt    time.Time         `json:"created_at"`
}

// SessionStore persists sessions as JSON files in dataDir.
type SessionStore struct {
	mu  sync.RWMutex
	dir string
}

func NewSessionStore(dir string) (*SessionStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create session dir: %w", err)
	}
	return &SessionStore{dir: dir}, nil
}

func (s *SessionStore) filePath(id string) string {
	return filepath.Join(s.dir, id+".json")
}

func (s *SessionStore) save(sess *Session) error {
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}
	return os.WriteFile(s.filePath(sess.ID), data, 0o644)
}

func (s *SessionStore) load(id string) (*Session, error) {
	data, err := os.ReadFile(s.filePath(id))
	if err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}
	return &sess, nil
}

func (s *SessionStore) Create() (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess := &Session{
		ID:        generateID(),
		Events:    []Event{},
		CreatedAt: time.Now(),
	}
	if err := s.save(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *SessionStore) Get(id string) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.load(id)
}

func (s *SessionStore) List() ([]*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}

	var sessions []*Session
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-5]
		sess, err := s.load(id)
		if err != nil {
			continue
		}
		sessions = append(sessions, sess)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].CreatedAt.After(sessions[j].CreatedAt)
	})
	return sessions, nil
}

func (s *SessionStore) EmitEvent(sessionID string, evt Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, err := s.load(sessionID)
	if err != nil {
		return err
	}
	if evt.ID == "" {
		evt.ID = generateID()
	}
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}
	sess.Events = append(sess.Events, evt)
	return s.save(sess)
}

func (s *SessionStore) UpdateSandbox(sessionID, sbxID, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, err := s.load(sessionID)
	if err != nil {
		return err
	}
	sess.SandboxID = sbxID
	sess.Token = token
	return s.save(sess)
}

// SetActiveSkills replaces the active skills list for a session.
func (s *SessionStore) SetActiveSkills(sessionID string, skills []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, err := s.load(sessionID)
	if err != nil {
		return err
	}
	sess.ActiveSkills = skills
	return s.save(sess)
}

// SetSkillArgs stores arguments for a specific skill in a session.
func (s *SessionStore) SetSkillArgs(sessionID, skillName, args string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, err := s.load(sessionID)
	if err != nil {
		return err
	}
	if sess.SkillArgs == nil {
		sess.SkillArgs = make(map[string]string)
	}
	if args == "" {
		delete(sess.SkillArgs, skillName)
	} else {
		sess.SkillArgs[skillName] = args
	}
	return s.save(sess)
}

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
