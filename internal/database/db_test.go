package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMalformedStoreIsNotTreatedAsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path); err == nil {
		t.Fatal("malformed store accepted as empty; a later save would overwrite it")
	}
	if b, _ := os.ReadFile(path); string(b) != "{not json" {
		t.Errorf("malformed store was modified: %q", b)
	}
}

func TestUnreadableStoreReturnsError(t *testing.T) {
	// A directory at the store path cannot be read as a file.
	if _, err := NewStore(t.TempDir()); err == nil {
		t.Fatal("expected error when store path is not a readable file")
	}
}

func TestMissingStoreStartsEmpty(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "none.json"))
	if err != nil || len(s.Sessions) != 0 {
		t.Fatalf("got %v, %v", s, err)
	}
}

func TestRoundTripAndAtomicSaveLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db.json")
	s, _ := NewStore(path)
	sess, err := s.CreateSession("one")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddMessage(&Message{SessionID: sess.ID, Role: "user", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	s2, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	msgs, _ := s2.GetMessages(sess.ID)
	if len(msgs) != 1 || msgs[0].Content != "hi" {
		t.Fatalf("reload lost data: %+v", msgs)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestFailedSaveKeepsOldFileAndRollsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "db.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	s, _ := NewStore(path)
	sess, err := s.CreateSession("keep")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)

	// Remove the directory so the next save cannot create its temp file.
	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMessage(&Message{SessionID: sess.ID, Role: "user", Content: "lost"}); err == nil {
		t.Fatal("AddMessage reported success although saving failed")
	}
	if msgs, _ := s.GetMessages(sess.ID); len(msgs) != 0 {
		t.Errorf("unsaved message left in memory: %+v", msgs)
	}
	if _, err := s.CreateSession("ghost"); err == nil {
		t.Fatal("CreateSession reported success although saving failed")
	}
	if list, _ := s.ListSessions(); len(list) != 1 {
		t.Errorf("unsaved session left in memory: %d sessions", len(list))
	}
	_ = before
}

func TestAddMessageRequiresSession(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "db.json"))
	if err := s.AddMessage(&Message{SessionID: "nope", Role: "user", Content: "x"}); err == nil {
		t.Error("message accepted for unknown session")
	}
}
