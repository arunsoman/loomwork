package aci

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SignatureStatus is the result of checking an archive's signature against
// the user's trusted keys.
type SignatureStatus int

const (
	SigMissing        SignatureStatus = iota // no signature present
	SigInvalid                               // present but does not verify
	SigValidUntrusted                        // verifies, but the signer is not trusted
	SigTrusted                               // verifies and the signer is trusted
)

func (s SignatureStatus) String() string {
	switch s {
	case SigMissing:
		return "no signature"
	case SigInvalid:
		return "signature INVALID"
	case SigValidUntrusted:
		return "signed by an untrusted key"
	default:
		return "signed by a trusted key"
	}
}

// Fingerprint is the full SHA-256 of the public key, hex encoded.
func (v *VerifyingKey) Fingerprint() string {
	h := sha256.Sum256(v.Pub)
	return hex.EncodeToString(h[:])
}

// ArchiveSigner returns the public key shipped in the archive. Its presence
// proves nothing about identity: use TrustStore to decide whether to accept it.
func ArchiveSigner(archive *Archive) (*VerifyingKey, error) {
	cert, ok := archive.Signatures[certPath]
	if !ok {
		return nil, fmt.Errorf("archive has no %s", certPath)
	}
	return LoadVerifyingKeyPEMBytes(cert)
}

// TrustStore is a directory of public keys the user has chosen to trust.
type TrustStore struct {
	Dir string
}

// DefaultTrustStore returns ~/.loomwork/trusted.
func DefaultTrustStore(home string) *TrustStore {
	return &TrustStore{Dir: filepath.Join(home, ".loomwork", "trusted")}
}

var keyIDRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

func (t *TrustStore) path(vk *VerifyingKey) string {
	return filepath.Join(t.Dir, vk.KeyID+".pem")
}

// Add trusts a key. Adding the same key again is a no-op.
func (t *TrustStore) Add(vk *VerifyingKey) error {
	if err := os.MkdirAll(t.Dir, 0o700); err != nil {
		return err
	}
	return vk.SavePEM(t.path(vk))
}

// Remove stops trusting the key with the given key ID.
func (t *TrustStore) Remove(keyID string) error {
	if !keyIDRe.MatchString(keyID) {
		return fmt.Errorf("invalid key id %q", keyID)
	}
	err := os.Remove(filepath.Join(t.Dir, keyID+".pem"))
	if os.IsNotExist(err) {
		return fmt.Errorf("no trusted key %s", keyID)
	}
	return err
}

// List returns all trusted keys.
func (t *TrustStore) List() ([]*VerifyingKey, error) {
	entries, err := os.ReadDir(t.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*VerifyingKey
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pem") {
			continue
		}
		vk, err := LoadVerifyingKeyPEM(filepath.Join(t.Dir, e.Name()))
		if err != nil {
			// A corrupt key file silently shrinks the trusted set; say so.
			fmt.Fprintf(os.Stderr, "⚠ trust store: ignoring unreadable key %s: %v\n", filepath.Join(t.Dir, e.Name()), err)
			continue
		}
		out = append(out, vk)
	}
	return out, nil
}

// IsTrusted reports whether vk (compared by full public key) is trusted.
func (t *TrustStore) IsTrusted(vk *VerifyingKey) bool {
	keys, _ := t.List()
	for _, k := range keys {
		if bytes.Equal(k.Pub, vk.Pub) {
			return true
		}
	}
	return false
}

// Check verifies the archive signature and looks the signer up in the store.
func (t *TrustStore) Check(archive *Archive) (SignatureStatus, *VerifyingKey) {
	if len(archive.Signatures) == 0 {
		return SigMissing, nil
	}
	if !VerifyArchiveSignature(archive) {
		return SigInvalid, nil
	}
	vk, err := ArchiveSigner(archive)
	if err != nil {
		return SigInvalid, nil
	}
	if t.IsTrusted(vk) {
		return SigTrusted, vk
	}
	return SigValidUntrusted, vk
}
