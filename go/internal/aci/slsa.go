package aci

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SLSAAttestation is an in-toto provenance statement describing a local build.
// It records what was built and where; it is only as trustworthy as the key
// that signs it (see SignSLSA / VerifySLSASigned).
type SLSAAttestation struct {
	Type          string        `json:"_type"`
	Subject       []SLSASubject `json:"subject"`
	PredicateType string        `json:"predicateType"`
	Predicate     SLSAPredicate `json:"predicate"`
	// Signer and Signature authenticate the statement (Ed25519 by the ACI signer
	// over the statement with Signature empty).
	Signer    string `json:"signer,omitempty"`
	Signature string `json:"signature,omitempty"`
}

type SLSASubject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

type SLSAPredicate struct {
	Builder     SLSABuilder     `json:"builder"`
	BuildType   string          `json:"buildType"`
	Invocation  SLSAInvocation  `json:"invocation"`
	BuildConfig SLSABuildConfig `json:"buildConfig"`
	Metadata    SLSAMetadata    `json:"metadata"`
	Materials   []SLSAMaterial  `json:"materials"`
}

type SLSABuilder struct {
	ID string `json:"id"`
}

type SLSAInvocation struct {
	ConfigSource SLSAConfigSource `json:"configSource"`
	Parameters   SLSAParameters   `json:"parameters"`
	Environment  SLSAEnvironment  `json:"environment"`
}

type SLSAConfigSource struct {
	URI        string            `json:"uri"`
	Digest     map[string]string `json:"digest"`
	EntryPoint string            `json:"entryPoint"`
}

type SLSAParameters struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
}

type SLSAEnvironment struct {
	BuildHost       string `json:"BUILD_HOST"`
	BuildUser       string `json:"BUILD_USER"`
	BuildTime       string `json:"BUILD_TIME"`
	LoomworkVersion string `json:"LOOMWORK_VERSION"`
}

type SLSABuildConfig struct {
	BuilderImage  string `json:"builderImage"`
	BuilderDigest string `json:"builderDigest"`
	BuildType     string `json:"buildType"`
}

type SLSAMetadata struct {
	BuildStartedOn  string           `json:"buildStartedOn"`
	BuildFinishedOn string           `json:"buildFinishedOn"`
	Completeness    SLSACompleteness `json:"completeness"`
	Reproducible    bool             `json:"reproducible"`
}

type SLSACompleteness struct {
	Parameters  bool `json:"parameters"`
	Environment bool `json:"environment"`
	Materials   bool `json:"materials"`
}

type SLSAMaterial struct {
	URI    string            `json:"uri"`
	Digest map[string]string `json:"digest"`
}

// BuildSLSAAttestation constructs a provenance statement for an ACI built on
// this machine. It makes no claim about builder isolation or reproducibility.
// subject digest is the SHA-256 of the packed .aci archive bytes.
func BuildSLSAAttestation(m *Manifest, archiveDigest string, sourceRepo string) *SLSAAttestation {
	now := time.Now().UTC().Format(time.RFC3339)
	host, _ := os.Hostname()
	user := os.Getenv("USER")
	if user == "" {
		user = "unknown"
	}

	return &SLSAAttestation{
		Type: "https://in-toto.io/Statement/v0.1",
		Subject: []SLSASubject{
			{
				Name:   fmt.Sprintf("%s-%s.aci", m.Metadata.Name, m.Metadata.Version),
				Digest: map[string]string{"sha256": stripSHA256(archiveDigest)},
			},
		},
		PredicateType: "https://slsa.dev/provenance/v0.2",
		Predicate: SLSAPredicate{
			Builder:   SLSABuilder{ID: "loomwork-buildkit://" + host},
			BuildType: "loomwork-build-v0.1",
			Invocation: SLSAInvocation{
				ConfigSource: SLSAConfigSource{
					URI:        "file://" + sourceRepo,
					Digest:     gitDigest(sourceRepo),
					EntryPoint: "manifest.json",
				},
				Parameters: SLSAParameters{
					Name:         m.Metadata.Name,
					Version:      m.Metadata.Version,
					Architecture: m.Metadata.Architecture,
					OS:           m.Metadata.OS,
				},
				Environment: SLSAEnvironment{
					BuildHost:       host,
					BuildUser:       user,
					BuildTime:       now,
					LoomworkVersion: "0.1.0",
				},
			},
			BuildConfig: SLSABuildConfig{
				BuilderImage: "local",
				BuildType:    "loomwork-build-v0.1",
			},
			Metadata: SLSAMetadata{
				BuildStartedOn:  now,
				BuildFinishedOn: now,
				Completeness:    SLSACompleteness{Parameters: true, Environment: false, Materials: false},
				Reproducible:    false,
			},
			Materials: []SLSAMaterial{
				{URI: "file://" + sourceRepo, Digest: gitDigest(sourceRepo)},
			},
		},
	}
}

// VerifySLSA checks that an attestation is well-formed and matches the
// expected archive digest.
func VerifySLSA(att *SLSAAttestation, expectedArchiveDigest string) bool {
	if att.Type != "https://in-toto.io/Statement/v0.1" {
		return false
	}
	if att.PredicateType != "https://slsa.dev/provenance/v0.2" {
		return false
	}
	if len(att.Subject) == 0 {
		return false
	}
	if att.Predicate.BuildType == "" {
		return false
	}
	if len(att.Predicate.Materials) == 0 {
		return false
	}
	got := att.Subject[0].Digest["sha256"]
	want := stripSHA256(expectedArchiveDigest)
	return got == want
}

// gitDigest returns {"gitCommit": <HEAD>} when dir is a git checkout, else an
// empty digest map.
func gitDigest(dir string) map[string]string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return map[string]string{}
	}
	return map[string]string{"gitCommit": strings.TrimSpace(string(out))}
}

func (a *SLSAAttestation) payload() ([]byte, error) {
	cp := *a
	cp.Signature = ""
	return json.Marshal(cp)
}

// SignSLSA signs the statement with the ACI signing key.
func SignSLSA(att *SLSAAttestation, sk *SigningKey) error {
	att.Signer = sk.KeyID
	p, err := att.payload()
	if err != nil {
		return err
	}
	att.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(sk.Priv, p))
	return nil
}

// VerifySLSASigned checks the statement's shape and subject digest AND that it
// was signed by vk (normally the ACI's signer, so the provenance and the
// archive are bound to one identity).
func VerifySLSASigned(att *SLSAAttestation, expectedArchiveDigest string, vk *VerifyingKey) bool {
	if !VerifySLSA(att, expectedArchiveDigest) || att.Signature == "" {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(att.Signature)
	if err != nil {
		return false
	}
	p, err := att.payload()
	if err != nil {
		return false
	}
	return ed25519.Verify(vk.Pub, p, sig)
}

// SaveSLSA writes the attestation as a sidecar file next to the .aci.
func SaveSLSA(att *SLSAAttestation, path string) error {
	data, err := json.MarshalIndent(att, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// LoadSLSA reads a sidecar attestation.
func LoadSLSA(path string) (*SLSAAttestation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var att SLSAAttestation
	if err := json.Unmarshal(data, &att); err != nil {
		return nil, err
	}
	return &att, nil
}

func stripSHA256(s string) string {
	if len(s) > 7 && s[:7] == "sha256:" {
		return s[7:]
	}
	return s
}

// SLSASidecarPath returns the conventional sidecar path for an .aci file.
func SLSASidecarPath(aciPath string) string {
	ext := filepath.Ext(aciPath)
	return aciPath[:len(aciPath)-len(ext)] + ".slsa.json"
}
