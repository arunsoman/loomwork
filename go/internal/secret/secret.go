// Package secret holds the at-rest encryption shared by the memory stores.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/pbkdf2"
)

// magic prefixes every encrypted value, so a value that fails to decrypt is
// reported as an error instead of being mistaken for legacy plaintext.
var magic = []byte("LWE1")

// ErrWrongKey means a value is encrypted but could not be decrypted.
var ErrWrongKey = errors.New("cannot decrypt memory: wrong or missing key (LOOMWORK_MEMORY_PASSPHRASE / ~/.loomwork/memory.key)")

// Box encrypts and decrypts values with AES-256-GCM. A nil-key Box stores
// plaintext and exists only for tests.
type Box struct {
	aead cipher.AEAD
}

// NewBox derives a key from passphrase and a per-database salt file
// (dbPath + ".salt", created on first use). An empty passphrase yields a
// plaintext Box.
func NewBox(dbPath, passphrase string) (*Box, error) {
	if passphrase == "" {
		return &Box{}, nil
	}
	saltPath := dbPath + ".salt"
	salt, err := os.ReadFile(saltPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		salt = make([]byte, 16)
		if _, err := io.ReadFull(rand.Reader, salt); err != nil {
			return nil, err
		}
		if err := os.WriteFile(saltPath, salt, 0o600); err != nil {
			return nil, err
		}
	}
	key := pbkdf2.Key([]byte(passphrase), salt, 200_000, 32, sha256.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: gcm}, nil
}

// Encrypted reports whether the Box actually encrypts.
func (b *Box) Encrypted() bool { return b.aead != nil }

// Seal encrypts plaintext (or returns it unchanged for a plaintext Box).
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	if b.aead == nil {
		return plaintext, nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := append([]byte{}, magic...)
	out = append(out, nonce...)
	return b.aead.Seal(out, nonce, plaintext, magic), nil
}

// Open reverses Seal. Values without the magic prefix are legacy plaintext
// written before encryption was enabled and are returned as-is.
func (b *Box) Open(blob []byte) ([]byte, error) {
	if len(blob) < len(magic) || string(blob[:len(magic)]) != string(magic) {
		return blob, nil
	}
	if b.aead == nil {
		return nil, ErrWrongKey
	}
	rest := blob[len(magic):]
	ns := b.aead.NonceSize()
	if len(rest) < ns+b.aead.Overhead() {
		return nil, fmt.Errorf("corrupt encrypted value")
	}
	pt, err := b.aead.Open(nil, rest[:ns], rest[ns:], magic)
	if err != nil {
		return nil, ErrWrongKey
	}
	return pt, nil
}

// MemoryPassphrase returns the passphrase for the default memory database:
// LOOMWORK_MEMORY_PASSPHRASE if set, else a random key created once at
// ~/.loomwork/memory.key (mode 0600).
func MemoryPassphrase(home string) (string, error) {
	if p := os.Getenv("LOOMWORK_MEMORY_PASSPHRASE"); p != "" {
		return p, nil
	}
	dir := filepath.Join(home, ".loomwork")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	keyPath := DefaultKeyPath(home)
	raw, err := os.ReadFile(keyPath)
	if os.IsNotExist(err) {
		b := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, b); err != nil {
			return "", err
		}
		raw = []byte(hex.EncodeToString(b))
		if err := os.WriteFile(keyPath, raw, 0o600); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	return string(raw), nil
}

// DefaultDBPath is ~/.loomwork/memory.db.
func DefaultDBPath(home string) string {
	return filepath.Join(home, ".loomwork", "memory.db")
}

// DefaultKeyPath is ~/.loomwork/memory.key.
func DefaultKeyPath(home string) string {
	return filepath.Join(home, ".loomwork", "memory.key")
}
