package verify

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loomwork.dev/loomwork/internal/aci"
)

func agentDir(t *testing.T) (string, *aci.Manifest) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"persona/system_prompt.md": "You help.",
		"skills/graph.json":        `{"skills":[{"name":"a","requires":[]}]}`,
		"tools/bindings.json":      `{"bindings":[{"name":"fs","mcpServer":"stdio:///bin/x","allowedTools":["read_file"]}]}`,
		"memory-schema.json":       `{}`,
		"sandbox.json":             `{}`,
	}
	m := &aci.Manifest{APIVersion: aci.APIVersion, Kind: aci.Kind,
		Metadata: aci.Metadata{Name: "t", Version: "v1.0.0", Architecture: "amd64", OS: "any"},
		Persona:  aci.PersonaRef{SystemPrompt: "persona/system_prompt.md"}, Skills: aci.SkillsRef{Graph: "skills/graph.json"},
		Tools: aci.ToolsRef{Bindings: "tools/bindings.json"}, Memory: aci.MemoryRef{Schema: "memory-schema.json"},
		Sandbox: aci.SandboxRef{Spec: "sandbox.json"}, Digests: map[string]string{}}
	for p, c := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644)
		m.Digests[p] = aci.Sha256Bytes([]byte(c))
	}
	return dir, m
}

func find(r *PropertyTestResult, name string) *Check {
	for i := range r.Checks {
		if r.Checks[i].Name == name {
			return &r.Checks[i]
		}
	}
	return nil
}

func TestPropertyTestsPassOnValidAgent(t *testing.T) {
	dir, m := agentDir(t)
	r := RunPropertyTests(dir, m)
	if !r.Passed {
		t.Fatalf("valid agent failed: %+v", r.Checks)
	}
}

func TestPropertyTestsCatchRealProblems(t *testing.T) {
	cases := map[string]struct {
		file, content, check string
	}{
		"tampered file":   {"persona/system_prompt.md", "changed", "referenced_files_match"},
		"cyclic skills":   {"skills/graph.json", `{"skills":[{"name":"a","requires":["a"]}]}`, "skills_graph"},
		"bad binding url": {"tools/bindings.json", `{"bindings":[{"name":"fs","mcpServer":""}]}`, "tool_bindings"},
		"bad sandbox":     {"sandbox.json", `not json`, "sandbox_spec"},
	}
	for name, c := range cases {
		dir, m := agentDir(t)
		os.WriteFile(filepath.Join(dir, c.file), []byte(c.content), 0o644)
		if c.check != "referenced_files_match" {
			m.Digests[c.file] = aci.Sha256Bytes([]byte(c.content))
		}
		r := RunPropertyTests(dir, m)
		ck := find(r, c.check)
		if r.Passed || ck == nil || ck.Passed {
			t.Errorf("%s: expected %s to fail, got %+v", name, c.check, r.Checks)
		}
	}
}

func TestMissingFileIsNotReportedAsAllPresent(t *testing.T) {
	dir, m := agentDir(t)
	os.Remove(filepath.Join(dir, "sandbox.json"))
	r := RunPropertyTests(dir, m)
	for _, c := range r.Checks {
		if c.Name == "referenced_files_match" && c.Passed {
			t.Fatal("a missing file must not produce a passing 'present' check")
		}
	}
}

func TestSBOMOnlyListsManifestFiles(t *testing.T) {
	dir, m := agentDir(t)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET"), 0o644)
	os.WriteFile(filepath.Join(dir, "old.aci"), []byte("x"), 0o644)
	s, err := BuildSBOM(dir, m)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range s.Files {
		if strings.Contains(f.Path, ".env") || strings.HasSuffix(f.Path, ".aci") {
			t.Fatalf("SBOM lists an unrelated file: %s", f.Path)
		}
	}
	if len(s.Files) != len(m.Digests) {
		t.Fatalf("want %d files, got %d", len(m.Digests), len(s.Files))
	}
}

func TestAttestationBoundToArchive(t *testing.T) {
	dir, m := agentDir(t)
	pub, priv, _ := ed25519.GenerateKey(nil)
	s, _ := BuildSBOM(dir, m)
	att, err := BuildAttestation(s, RunPropertyTests(dir, m), aci.Sha256Bytes([]byte("archive-1")), "kid", priv)
	if err != nil {
		t.Fatal(err)
	}
	if !att.Verify(pub) {
		t.Fatal("valid attestation should verify")
	}
	att.ArchiveDigest = aci.Sha256Bytes([]byte("archive-2"))
	if att.Verify(pub) {
		t.Fatal("changing the archive digest must invalidate the signature")
	}
}
