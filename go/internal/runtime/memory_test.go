package runtime

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultMemoryEncryptedAtRest(t *testing.T) {
	t.Setenv("LOOMWORK_MEMORY_PASSPHRASE", "")
	home := t.TempDir()
	m, err := OpenDefaultMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Write(&MemoryEntry{AgentID: "a", ACI: "a@v1", Kind: "episode", Content: "favorite color is teal"}); err != nil {
		t.Fatal(err)
	}
	m.Close()

	raw, _ := os.ReadFile(filepath.Join(home, ".loomwork", "memory.db"))
	if bytes.Contains(raw, []byte("teal")) {
		t.Fatal("plaintext found in memory.db")
	}
	m, err = OpenDefaultMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	got, err := m.QueryByAgent("a", 10)
	if err != nil || len(got) != 1 || got[0].Content != "favorite color is teal" {
		t.Fatalf("roundtrip failed: %v %v", got, err)
	}
}

func TestMemoryWriteDoesNotDuplicateTimeline(t *testing.T) {
	m, err := OpenMemory(filepath.Join(t.TempDir(), "m.db"), "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for i := 0; i < 3; i++ {
		if err := m.Write(&MemoryEntry{ID: "folder_index_x", AgentID: "a", ACI: "a@v1", Kind: "folder_index", Content: "v"}); err != nil {
			t.Fatal(err)
		}
	}
	tl, err := m.Timeline(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl) != 1 {
		t.Fatalf("timeline has %d rows for one entry, want 1", len(tl))
	}
}

func TestPassphraseFromEnvDecrypts(t *testing.T) {
	t.Setenv("LOOMWORK_MEMORY_PASSPHRASE", "correct horse")
	home := t.TempDir()
	m, err := OpenDefaultMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	m.Write(&MemoryEntry{AgentID: "a", ACI: "a@v1", Kind: "user_message", Content: "hello"})
	m.Close()
	m2, err := OpenDefaultMemory(home)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := m2.QueryConversation("a", 5)
	m2.Close()
	if len(got) != 1 || got[0].Content != "hello" {
		t.Fatalf("same passphrase should read the entry back, got %+v", got)
	}
	t.Setenv("LOOMWORK_MEMORY_PASSPHRASE", "wrong")
	if m3, err := OpenDefaultMemory(home); err == nil {
		if rows, qerr := m3.QueryConversation("a", 5); qerr == nil && len(rows) > 0 {
			t.Fatal("a different passphrase must not decrypt the entries")
		}
		m3.Close()
	}
}
