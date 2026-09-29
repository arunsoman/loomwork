package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCrossCompatWithPython verifies that a .aci produced by the Go impl
// can be loaded, unpacked, and verified by the Python v0.1 reference impl
// at github.com/loomwork/loomwork (Python).
//
// This test is skipped if the Python impl is not on PYTHONPATH.
func TestCrossCompatWithPython(t *testing.T) {
	pySrc := os.Getenv("LOOMWORK_PYTHON_SRC")
	if pySrc == "" {
		pySrc = "/home/z/my-project/loomwork-src"
	}
	if _, err := os.Stat(filepath.Join(pySrc, "loomwork", "aci", "archive.py")); err != nil {
		t.Skipf("Python impl not found at %s (set LOOMWORK_PYTHON_SRC)", pySrc)
	}

	// Build a Go-produced ACI in a temp dir
	dir := t.TempDir()
	lw := buildLoomwork(t)
	cmd := exec.Command(lw, "init", dir+"/test-agent")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(lw, "package", "--out", dir+"/test-agent/test.aci",
		"--signing-key", os.Getenv("HOME")+"/.loomwork/key.pem")
	cmd.Dir = dir + "/test-agent"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("package failed: %v\n%s", err, out)
	}

	// Now have Python load + verify it
	pyScript := `
import json, sys, tempfile, os
sys.path.insert(0, "` + pySrc + `")
from loomwork.aci.archive import AciArchive
from loomwork.aci.signing import verify_aci_signature

aci_path = sys.argv[1]
data = open(aci_path, 'rb').read()
archive = AciArchive.from_tar_gz(data)
print(f"Python loaded ACI: {archive.manifest.metadata.name}@{archive.manifest.metadata.version}")
print(f"  digests verified: {len(archive.manifest.digests)}")

# Unpack and verify signature
with tempfile.TemporaryDirectory() as tmp:
    from loomwork.aci.archive import unpack_aci
    unpack_aci(aci_path, tmp)
    if verify_aci_signature(tmp):
        print("Python verified signature: OK")
    else:
        print("Python verified signature: FAILED")
        sys.exit(1)
`
	cmd = exec.Command("python3", "-c", pyScript, dir+"/test-agent/test.aci")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Python failed to load Go ACI: %v\n%s", err, out)
	}
	t.Logf("Python output:\n%s", out)
}

// TestManifestJSONShape matches the Python manifest field-by-field.
func TestManifestJSONShape(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(buildLoomwork(t), "init", dir+"/test-agent")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "test-agent", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	// Required top-level fields per PRD §4.3
	required := []string{
		"apiVersion", "kind", "metadata", "persona", "skills",
		"tools", "memory", "sandbox", "digests",
	}
	for _, key := range required {
		if _, ok := m[key]; !ok {
			t.Errorf("manifest missing required field: %s", key)
		}
	}
	// apiVersion must be exactly the spec string
	if m["apiVersion"] != "aci.loomwork.dev/v0.1" {
		t.Errorf("apiVersion wrong: %v", m["apiVersion"])
	}
	if m["kind"] != "Agent" {
		t.Errorf("kind wrong: %v", m["kind"])
	}
	// memory.schema alias
	mem, _ := m["memory"].(map[string]interface{})
	if mem == nil || mem["schema"] == nil {
		t.Errorf("memory.schema missing")
	}
}

// buildLoomwork compiles the CLI into a temp dir so tests don't depend on a
// prebuilt binary at a fixed path.
func buildLoomwork(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "loomwork")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build loomwork: %v\n%s", err, out)
	}
	return bin
}
