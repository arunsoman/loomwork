package aci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalManifest writes a valid manifest + supporting files to dir.
func minimalManifest(t *testing.T, dir string) *Manifest {
	t.Helper()
	m := &Manifest{
		APIVersion: APIVersion,
		Kind:       Kind,
		Metadata: Metadata{
			Name: "test-agent", Version: "v0.1.0",
			Architecture: "amd64", OS: "linux",
		},
		Persona: PersonaRef{SystemPrompt: "persona/system_prompt.md"},
		Skills:  SkillsRef{Graph: "skills/graph.json"},
		Tools:   ToolsRef{Bindings: "tools/bindings.json"},
		Memory:  MemoryRef{Schema: "memory-schema.json"},
		Sandbox: SandboxRef{Spec: "sandbox.json"},
		Digests: map[string]string{},
	}
	files := map[string]string{
		"persona/system_prompt.md": "You are a test agent.",
		"skills/graph.json":        `{"skills":[]}`,
		"tools/bindings.json":      `{"bindings":[]}`,
		"memory-schema.json":       `{"stores":{}}`,
		"sandbox.json":             `{"fs.read":["**"]}`,
	}
	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		m.Digests[path] = Sha256Bytes([]byte(content))
	}
	return m
}

func TestManifestValidate(t *testing.T) {
	dir := t.TempDir()
	m := minimalManifest(t, dir)
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestManifestRejectsBadAPIVersion(t *testing.T) {
	m := &Manifest{APIVersion: "aci.loomwork.dev/v0.2", Kind: Kind,
		Metadata: Metadata{Name: "x", Version: "v0.1.0", Architecture: "amd64", OS: "linux"}}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestManifestRejectsBadName(t *testing.T) {
	m := &Manifest{APIVersion: APIVersion, Kind: Kind,
		Metadata: Metadata{Name: "BadName", Version: "v0.1.0", Architecture: "amd64", OS: "linux"}}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected error for uppercase name")
	}
}

func TestManifestRejectsMissingDigest(t *testing.T) {
	dir := t.TempDir()
	m := minimalManifest(t, dir)
	delete(m.Digests, "sandbox.json")
	err := m.Validate()
	if err == nil {
		t.Fatal("expected error for missing digest")
	}
}

func TestManifestRejectsBadDigestFormat(t *testing.T) {
	dir := t.TempDir()
	m := minimalManifest(t, dir)
	m.Digests["sandbox.json"] = "md5:abc"
	err := m.Validate()
	if err == nil {
		t.Fatal("expected error for bad digest format")
	}
}

func TestManifestCanonicalJSONDeterministic(t *testing.T) {
	dir := t.TempDir()
	m := minimalManifest(t, dir)
	a, err := m.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("canonical JSON not deterministic")
	}
}

func TestArchiveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := minimalManifest(t, dir)
	data, err := m.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	archive, err := ArchiveFromDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	tarGz, err := archive.ToTarGz()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ArchiveFromTarGz(tarGz)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Manifest.Metadata.Name != "test-agent" {
		t.Fatalf("name mismatch: %s", parsed.Manifest.Metadata.Name)
	}
}

func TestSigningRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := minimalManifest(t, dir)
	sk, err := GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	env, err := SignManifest(m, sk)
	if err != nil {
		t.Fatal(err)
	}
	vk := sk.VerifyingKey()
	if !VerifyManifest(m, env, vk) {
		t.Fatal("signature verification failed")
	}
}

func TestSigningRejectsTampered(t *testing.T) {
	dir := t.TempDir()
	m := minimalManifest(t, dir)
	sk, _ := GenerateSigningKey()
	env, _ := SignManifest(m, sk)
	// Tamper
	m.Metadata.Description = "TAMPERED"
	vk := sk.VerifyingKey()
	if VerifyManifest(m, env, vk) {
		t.Fatal("expected verification to fail on tampered manifest")
	}
}

func TestSLSARoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := minimalManifest(t, dir)
	fakeDigest := "sha256:" + strings.Repeat("0", 64) + "abc"
	att := BuildSLSAAttestation(m, fakeDigest, "/tmp/test")
	if !VerifySLSA(att, fakeDigest) {
		t.Fatal("SLSA verification failed")
	}
}

func TestSLSARejectsWrongDigest(t *testing.T) {
	dir := t.TempDir()
	m := minimalManifest(t, dir)
	att := BuildSLSAAttestation(m, "sha256:abc", "/tmp/test")
	if VerifySLSA(att, "sha256:xyz") {
		t.Fatal("expected SLSA verification to fail on wrong digest")
	}
}
