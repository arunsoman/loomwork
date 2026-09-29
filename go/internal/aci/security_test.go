package aci

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildDir writes a valid agent directory and returns it with its manifest.
func buildDir(t *testing.T) (string, *Manifest) {
	t.Helper()
	dir := t.TempDir()
	m := minimalManifest(t, dir)
	j, _ := m.ToJSON()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), j, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, m
}

func tarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for n, d := range files {
		if err := tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeReg, Size: int64(len(d)), Mode: 0o644}); err != nil {
			t.Fatal(err)
		}
		tw.Write(d)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func packedFiles(t *testing.T, dir string, m *Manifest) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	mj, _ := m.ToJSON()
	files["manifest.json"] = mj
	for p := range m.Digests {
		d, _ := os.ReadFile(filepath.Join(dir, p))
		files[p] = d
	}
	return files
}

// A signature made with an unknown key verifies cryptographically but is not
// trusted until the user adds the key.
func TestSignerMustBeTrusted(t *testing.T) {
	dir, _ := buildDir(t)
	attacker, _ := GenerateSigningKey()
	if err := SignArchiveInPlace(dir, attacker); err != nil {
		t.Fatal(err)
	}
	a, err := ArchiveFromDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	ts := &TrustStore{Dir: filepath.Join(t.TempDir(), "trusted")}
	status, vk := ts.Check(a)
	if status != SigValidUntrusted || vk.KeyID != attacker.KeyID {
		t.Fatalf("want valid-but-untrusted, got %v", status)
	}
	if err := ts.Add(attacker.VerifyingKey()); err != nil {
		t.Fatal(err)
	}
	if status, _ := ts.Check(a); status != SigTrusted {
		t.Fatalf("want trusted after add, got %v", status)
	}
	if err := ts.Remove(attacker.KeyID); err != nil {
		t.Fatal(err)
	}
	if status, _ := ts.Check(a); status != SigValidUntrusted {
		t.Fatalf("want untrusted after remove, got %v", status)
	}
}

func TestMissingAndTamperedSignature(t *testing.T) {
	dir, _ := buildDir(t)
	a, _ := ArchiveFromDirectory(dir)
	ts := &TrustStore{Dir: t.TempDir()}
	if s, _ := ts.Check(a); s != SigMissing {
		t.Fatalf("want missing, got %v", s)
	}
	sk, _ := GenerateSigningKey()
	SignArchiveInPlace(dir, sk)
	a, _ = ArchiveFromDirectory(dir)
	a.Manifest.Metadata.Description = "changed after signing"
	if s, _ := ts.Check(a); s != SigInvalid {
		t.Fatalf("want invalid, got %v", s)
	}
}

func TestLoaderRejectsUnsafeNames(t *testing.T) {
	dir, m := buildDir(t)
	for _, name := range []string{"../../escaped.txt", "/abs/path", "a/../../b", "x\\y", "./dot"} {
		files := packedFiles(t, dir, m)
		files[name] = []byte("x")
		if _, err := ArchiveFromTarGz(tarGz(t, files)); err == nil {
			t.Errorf("entry %q should be rejected", name)
		}
	}
}

func TestLoaderRejectsUnlistedFiles(t *testing.T) {
	dir, m := buildDir(t)
	files := packedFiles(t, dir, m)
	files["extra.sh"] = []byte("rm -rf")
	if _, err := ArchiveFromTarGz(tarGz(t, files)); err == nil || !strings.Contains(err.Error(), "does not list") {
		t.Fatalf("unlisted file must be rejected, got %v", err)
	}
	files = packedFiles(t, dir, m)
	files["signatures/other.sig"] = []byte("x")
	if _, err := ArchiveFromTarGz(tarGz(t, files)); err == nil {
		t.Fatal("unexpected signature entry must be rejected")
	}
}

func TestLoaderLimits(t *testing.T) {
	dir, m := buildDir(t)
	files := packedFiles(t, dir, m)
	files["persona/system_prompt.md"] = bytes.Repeat([]byte("a"), MaxFileBytes+1)
	if _, err := ArchiveFromTarGz(tarGz(t, files)); err == nil {
		t.Fatal("oversized file must be rejected")
	}
}

func TestUnpackStaysInsideTarget(t *testing.T) {
	dir, m := buildDir(t)
	a, _ := ArchiveFromDirectory(dir)
	a.Files["../../escaped.txt"] = []byte("pwn")
	_ = m
	out := filepath.Join(t.TempDir(), "a", "b")
	if err := a.Unpack(out); err == nil {
		t.Fatal("unpack must refuse a path that escapes the target")
	}
	if _, err := os.Stat(filepath.Join(out, "..", "..", "escaped.txt")); err == nil {
		t.Fatal("file was written outside the target directory")
	}
}

// Only files the manifest names are packed; stray files are left out.
func TestPackOnlyListedFiles(t *testing.T) {
	dir, _ := buildDir(t)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("API_KEY=hunter2"), 0o644)
	os.WriteFile(filepath.Join(dir, "old.aci"), []byte("previous build"), 0o644)
	a, err := ArchiveFromDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Files[".env"]; ok {
		t.Fatal(".env must not be packed")
	}
	if _, ok := a.Files["old.aci"]; ok {
		t.Fatal("stray build output must not be packed")
	}
	data, _ := a.ToTarGz()
	if bytes.Contains(data, []byte("hunter2")) {
		t.Fatal("secret found in packed archive")
	}
}

func TestManifestRequiresAllReferences(t *testing.T) {
	dir := t.TempDir()
	m := minimalManifest(t, dir)
	m.Sandbox.Spec = ""
	if err := m.Validate(); err == nil {
		t.Fatal("empty file reference must be rejected")
	}
}

func TestCanonicalJSONKeyOrderAndNumbers(t *testing.T) {
	var b strings.Builder
	in := map[string]interface{}{"b": json.Number("1"), "a": "x<y", "é": []interface{}{true, nil}, "\U0001F600": json.Number("2")}
	if err := encodeCanonical(&b, canonicalize(in)); err != nil {
		t.Fatal(err)
	}
	// UTF-16 order: "a" < "b" < "é" (U+00E9) < U+1F600 (surrogates 0xD83D..)
	want := `{"a":"x<y","b":1,"é":[true,null],"😀":2}`
	if b.String() != want {
		t.Fatalf("got  %s\nwant %s", b.String(), want)
	}
	for in, want := range map[string]string{"1e21": "1e+21", "0.5": "0.5", "-0": "0", "100": "100", "1E2": "100"} {
		got, err := canonicalNumber(json.Number(in))
		if err != nil || got != want {
			t.Errorf("number %s: got %q (%v), want %q", in, got, err, want)
		}
	}
}

func TestSLSASignedBinding(t *testing.T) {
	dir := t.TempDir()
	m := minimalManifest(t, dir)
	sk, _ := GenerateSigningKey()
	digest := Sha256Bytes([]byte("archive"))
	att := BuildSLSAAttestation(m, digest, dir)
	if VerifySLSASigned(att, digest, sk.VerifyingKey()) {
		t.Fatal("unsigned statement must not verify")
	}
	if err := SignSLSA(att, sk); err != nil {
		t.Fatal(err)
	}
	if !VerifySLSASigned(att, digest, sk.VerifyingKey()) {
		t.Fatal("signed statement should verify")
	}
	other, _ := GenerateSigningKey()
	if VerifySLSASigned(att, digest, other.VerifyingKey()) {
		t.Fatal("statement must not verify under another key")
	}
	att.Predicate.BuildType = "forged"
	if VerifySLSASigned(att, digest, sk.VerifyingKey()) {
		t.Fatal("modified statement must not verify")
	}
}
