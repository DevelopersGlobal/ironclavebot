package database

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Message represents a single chat or agent event
type Message struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Role      string    `json:"role"` // system, user, assistant, tool
	Content   string    `json:"content"`
	ToolCalls string    `json:"tool_calls,omitempty"`
	Timestamp time.Time `json:"timestamp"`
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
	Sessions map[string]*Session  `json:"sessions"`
	Messages map[string][]*Message `json:"messages"`
}

// NewStore initializes embedded persistence
func NewStore(path string) (*FileStore, error) {
	store := &FileStore{
		filePath: path,
		Sessions: make(map[string]*Session),
		Messages: make(map[string][]*Message),
	}

	if _, err := os.Stat(path); err == nil {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			_ = json.Unmarshal(data, store)
		}
	}

	return store, nil
}

func (s *FileStore) save() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath, data, 0644)
}

func (s *FileStore) CreateSession(title string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := fmt.Sprintf("session-%d", time.Now().UnixNano())
	session := &Session{
		ID:        id,
		Title:     title,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	s.Sessions[id] = session
	s.Messages[id] = []*Message{}
	_ = s.save()
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
	return list, nil
}

func (s *FileStore) AddMessage(msg *Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	s.Messages[msg.SessionID] = append(s.Messages[msg.SessionID], msg)
	return s.save()
}

func (s *FileStore) GetMessages(sessionID string) ([]*Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	msgs, ok := s.Messages[sessionID]
	if !ok {
		return []*Message{}, nil
	}
	return msgs, nil
}
