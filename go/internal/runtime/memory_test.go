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
