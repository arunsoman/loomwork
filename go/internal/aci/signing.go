package aci

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
)

// SigningKey is an Ed25519 private key with a short key ID (first 16 hex
// chars of the public key). The signature envelope is cosign-compatible
// in shape — same JSON structure as the Python reference impl produces.
type SigningKey struct {
	Priv  ed25519.PrivateKey
	KeyID string
}

// VerifyingKey is an Ed25519 public key.
type VerifyingKey struct {
	Pub   ed25519.PublicKey
	KeyID string
}

// GenerateSigningKey creates a new Ed25519 key pair.
func GenerateSigningKey() (*SigningKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &SigningKey{
		Priv:  priv,
		KeyID: keyIDFromPub(pub),
	}, nil
}

func keyIDFromPub(pub ed25519.PublicKey) string {
	return fmt.Sprintf("%x", pub)[:16]
}

// VerifyingKey returns the public counterpart.
func (s *SigningKey) VerifyingKey() *VerifyingKey {
	pub := s.Priv.Public().(ed25519.PublicKey)
	return &VerifyingKey{Pub: pub, KeyID: s.KeyID}
}

// SavePEM writes the private key as PKCS8 PEM.
func (s *SigningKey) SavePEM(path string) error {
	der, err := marshalPKCS8PrivateKey(s.Priv)
	if err != nil {
		return err
	}
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	return os.WriteFile(path, pem.EncodeToMemory(block), 0o600)
}

// LoadSigningKeyPEM reads a PKCS8 PEM private key.
func LoadSigningKeyPEM(path string) (*SigningKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block in %s", path)
	}
	priv, err := parsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ed, ok := priv.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not an Ed25519 key: %T", priv)
	}
	pub := ed.Public().(ed25519.PublicKey)
	return &SigningKey{Priv: ed, KeyID: keyIDFromPub(pub)}, nil
}

// SavePublicPEM writes the public key as SPKI PEM.
func (v *VerifyingKey) SavePEM(path string) error {
	der, err := marshalPKIXPublicKey(v.Pub)
	if err != nil {
		return err
	}
	block := &pem.Block{Type: "PUBLIC KEY", Bytes: der}
	return os.WriteFile(path, pem.EncodeToMemory(block), 0o644)
}

// LoadVerifyingKeyPEM reads an SPKI PEM public key from a file.
func LoadVerifyingKeyPEM(path string) (*VerifyingKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return LoadVerifyingKeyPEMBytes(data)
}

// LoadVerifyingKeyPEMBytes reads an SPKI PEM public key from a byte slice.
func LoadVerifyingKeyPEMBytes(data []byte) (*VerifyingKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block")
	}
	pub, err := parsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ed, ok := pub.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an Ed25519 key: %T", pub)
	}
	return &VerifyingKey{Pub: ed, KeyID: keyIDFromPub(ed)}, nil
}

// SignatureEnvelope is the cosign-compatible signature envelope.
// Same shape as the Python reference impl writes.
type SignatureEnvelope struct {
	Critical struct {
		Identity struct {
			DockerReference string `json:"docker-reference"`
		} `json:"identity"`
		Type string `json:"type"`
	} `json:"critical"`
	Optional struct {
		Issuer  string `json:"issuer"`
		Subject string `json:"subject"`
	} `json:"optional"`
	Base64Signature string `json:"base64Signature"`
	PayloadHash     string `json:"payloadHash"`
}

// SignManifest produces a signature envelope over the manifest's canonical JSON.
func SignManifest(m *Manifest, sk *SigningKey) (*SignatureEnvelope, error) {
	payload, err := m.CanonicalJSON()
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(sk.Priv, payload)

	env := &SignatureEnvelope{}
	env.Critical.Identity.DockerReference = "loomwork.dev/" + sk.KeyID
	env.Critical.Type = "cosign container image signature"
	env.Optional.Issuer = "loomwork-local"
	env.Optional.Subject = sk.KeyID
	env.Base64Signature = base64.StdEncoding.EncodeToString(sig)
	env.PayloadHash = Sha256Bytes(payload)
	return env, nil
}

// VerifyManifest checks a signature envelope against a verifying key.
func VerifyManifest(m *Manifest, env *SignatureEnvelope, vk *VerifyingKey) bool {
	if env.Optional.Subject != vk.KeyID {
		return false
	}
	payload, err := m.CanonicalJSON()
	if err != nil {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(env.Base64Signature)
	if err != nil {
		return false
	}
	return ed25519.Verify(vk.Pub, payload, sig)
}

// SignArchiveInPlace writes signatures/manifest.{sig,cert} into sourceDir.
func SignArchiveInPlace(sourceDir string, sk *SigningKey) error {
	manifestPath := filepath.Join(sourceDir, "manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	m, err := ParseManifest(manifestData)
	if err != nil {
		return err
	}
	env, err := SignManifest(m, sk)
	if err != nil {
		return err
	}
	sigJSON, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	sigDir := filepath.Join(sourceDir, "signatures")
	if err := os.MkdirAll(sigDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(sigDir, "manifest.sig"),
		append(sigJSON, '\n'), 0o644); err != nil {
		return err
	}
	return sk.VerifyingKey().SavePEM(filepath.Join(sigDir, "manifest.cert"))
}

// VerifyArchiveSignature reports whether the archive's signature verifies
// against the public key shipped inside the archive. It does NOT establish who
// signed it; use TrustStore.Check for that.
func VerifyArchiveSignature(archive *Archive) bool {
	sigBytes, ok := archive.Signatures[sigPath]
	if !ok {
		return false
	}
	var env SignatureEnvelope
	if err := json.Unmarshal(sigBytes, &env); err != nil {
		return false
	}
	vk, err := ArchiveSigner(archive)
	if err != nil {
		return false
	}
	return VerifyManifest(archive.Manifest, &env, vk)
}
