package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Message represents a single chat or agent event
type Message struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Role      string `json:"role"` // system, user, assistant, tool
	Content   string `json:"content"`
	// ToolCalls is the JSON array of tool calls requested by an assistant message.
	ToolCalls string `json:"tool_calls,omitempty"`
	// ToolCallID links a tool result to the assistant tool call it answers.
	ToolCallID string    `json:"tool_call_id,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}

// Session represents an agent workspace or persistent teammate session
type Session struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	SystemRules string    `json:"system_rules"`
}

// Store defines persistence operations for IronClaveBot
type Store interface {
	CreateSession(title string) (*Session, error)
	GetSession(id string) (*Session, error)
	ListSessions() ([]*Session, error)
	AddMessage(msg *Message) error
	GetMessages(sessionID string) ([]*Message, error)
}

// FileStore provides a lightweight, zero-dependency embedded persistent engine
type FileStore struct {
	mu       sync.RWMutex
	filePath string
	Sessions map[string]*Session   `json:"sessions"`
	Messages map[string][]*Message `json:"messages"`
}

// NewStore initializes embedded persistence. A missing file starts an empty
// store; any other read or decode failure is returned so a later save cannot
// overwrite history that merely failed to load.
func NewStore(path string) (*FileStore, error) {
	store := &FileStore{
		filePath: path,
		Sessions: make(map[string]*Session),
		Messages: make(map[string][]*Message),
	}

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(data) > 0 {
			if err := json.Unmarshal(data, store); err != nil {
				return nil, fmt.Errorf("failed to decode store %s: %w", path, err)
			}
		}
	case errors.Is(err, os.ErrNotExist):
		// First run: start empty.
	default:
		return nil, fmt.Errorf("failed to read store %s: %w", path, err)
	}

	if store.Sessions == nil {
		store.Sessions = make(map[string]*Session)
	}
	if store.Messages == nil {
		store.Messages = make(map[string][]*Message)
	}
	return store, nil
}

// save writes the store atomically: a temp file in the same directory is
// flushed and closed, then renamed over the old copy. The caller holds s.mu.
func (s *FileStore) save() (err error) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(s.filePath)
	tmp, err := os.CreateTemp(dir, filepath.Base(s.filePath)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, s.filePath)
}

func (s *FileStore) CreateSession(title string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	id := fmt.Sprintf("session-%d", now.UnixNano())
	session := &Session{
		ID:        id,
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}

	s.Sessions[id] = session
	s.Messages[id] = []*Message{}
	if err := s.save(); err != nil {
		delete(s.Sessions, id)
		delete(s.Messages, id)
		return nil, fmt.Errorf("failed to persist session: %w", err)
	}
	return session, nil
}

func (s *FileStore) GetSession(id string) (*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, exists := s.Sessions[id]
	if !exists {
		return nil, fmt.Errorf("session not found: %s", id)
	}
	return session, nil
}

func (s *FileStore) ListSessions() ([]*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]*Session, 0, len(s.Sessions))
	for _, sess := range s.Sessions {
		list = append(list, sess)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.Before(list[j].CreatedAt)
	})
	return list, nil
}

// AddMessage appends a message to an existing session. If the write to disk
// fails the message is removed again so memory and disk stay consistent.
func (s *FileStore) AddMessage(msg *Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.Sessions[msg.SessionID]
	if !ok {
		return fmt.Errorf("session not found: %s", msg.SessionID)
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	prevUpdated := sess.UpdatedAt
	prev := s.Messages[msg.SessionID]
	s.Messages[msg.SessionID] = append(prev, msg)
	sess.UpdatedAt = msg.Timestamp
	if err := s.save(); err != nil {
		s.Messages[msg.SessionID] = prev
		sess.UpdatedAt = prevUpdated
		return fmt.Errorf("failed to persist message: %w", err)
	}
	return nil
}

// GetMessages returns a copy of the session's messages.
func (s *FileStore) GetMessages(sessionID string) ([]*Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	msgs, ok := s.Messages[sessionID]
	if !ok {
		return []*Message{}, nil
	}
	out := make([]*Message, len(msgs))
	copy(out, msgs)
	return out, nil
}
