package secret

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestBoxRoundTripAndWrongKey(t *testing.T) {
	db := filepath.Join(t.TempDir(), "m.db")
	b, err := NewBox(db, "pw")
	if err != nil {
		t.Fatal(err)
	}
	ct, _ := b.Seal([]byte("secret"))
	if bytes.Contains(ct, []byte("secret")) {
		t.Fatal("ciphertext contains plaintext")
	}
	if pt, err := b.Open(ct); err != nil || string(pt) != "secret" {
		t.Fatalf("roundtrip: %q %v", pt, err)
	}
	wrong, _ := NewBox(db, "other")
	if _, err := wrong.Open(ct); err != ErrWrongKey {
		t.Fatalf("wrong key must fail, got %v", err)
	}
	plain, _ := NewBox(filepath.Join(t.TempDir(), "p.db"), "")
	if _, err := plain.Open(ct); err != ErrWrongKey {
		t.Fatalf("missing key must fail, got %v", err)
	}
	// short/corrupt value with the magic prefix: error, not a panic
	if _, err := b.Open(append([]byte("LWE1"), 1, 2, 3)); err == nil {
		t.Fatal("corrupt value must fail")
	}
	// legacy plaintext (no prefix) still reads
	if pt, err := b.Open([]byte("old plain value")); err != nil || string(pt) != "old plain value" {
		t.Fatalf("legacy plaintext: %q %v", pt, err)
	}
	// tampering is detected
	ct[len(ct)-1] ^= 1
	if _, err := b.Open(ct); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}

func TestMemoryPassphrase(t *testing.T) {
	t.Setenv("LOOMWORK_MEMORY_PASSPHRASE", "")
	home := t.TempDir()
	a, err := MemoryPassphrase(home)
	if err != nil || len(a) < 32 {
		t.Fatalf("generated key: %q %v", a, err)
	}
	if b, _ := MemoryPassphrase(home); a != b {
		t.Fatal("key must be stable across calls")
	}
	t.Setenv("LOOMWORK_MEMORY_PASSPHRASE", "from-env")
	if c, _ := MemoryPassphrase(home); c != "from-env" {
		t.Fatal("env override ignored")
	}
}
