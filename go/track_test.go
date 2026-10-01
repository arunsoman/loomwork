package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R-42 to R-44, R-47 (Lane A part): tracking happens automatically on a
// normal command, writes nothing into the folder, records outside changes
// without acknowledging them, and `leave` removes it.
func TestLaneAEndToEnd(t *testing.T) {
	bin := buildBinary(t)
	e := newCLIEnv(t, bin, "http://127.0.0.1:1")
	dir := filepath.Join(e.home, "notes")
	os.MkdirAll(filepath.Join(dir, "skills"), 0o755)
	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("# Notes agent\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "skills", "x.md"), []byte("Do x.\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("## A\nb\n"), 0o644)
	before := hashTree(t, dir)

	// R-42, R-43: the first command tracks and says so once.
	first := mustRun(t, e, dir, nil, "package", "--out", "demo.aci")
	if !strings.Contains(first, "Tracking this folder") || !strings.Contains(first, "None of your files are touched") {
		t.Fatalf("first run should print the notice:\n%s", first)
	}
	if again := mustRun(t, e, dir, nil, "package", "--out", "demo.aci"); strings.Contains(again, "Tracking this folder") {
		t.Errorf("notice must appear once:\n%s", again)
	}
	shadowRoot := filepath.Join(e.home, ".loomwork", "folders")
	if ents, err := os.ReadDir(shadowRoot); err != nil || len(ents) != 1 {
		t.Fatalf("want one shadow store, got %v %v", ents, err)
	}
	mustRun(t, e, dir, nil, "check")

	// R-44: an outside edit is recorded automatically but check still fails.
	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("# Notes agent\nedited elsewhere\n"), 0o644)
	out, _, code := e.run(dir, "", "check")
	if code == 0 || !strings.Contains(out, "AGENT.md") {
		t.Fatalf("check must fail on unsealed change (exit %d):\n%s", code, out)
	}
	if _, _, code := e.run(dir, "", "check"); code == 0 {
		t.Error("recording a change must not acknowledge it: second check passed")
	}
	mustRun(t, e, dir, nil, "seal")
	mustRun(t, e, dir, nil, "check")

	// Nothing but the user's files, the archive and its sidecars is in the folder.
	after := hashTree(t, dir)
	allowed := map[string]bool{"demo.aci": true, "demo.slsa.json": true, "demo.aci.attestation.json": true}
	for name := range after {
		if _, ok := before[name]; !ok && !allowed[name] {
			t.Errorf("tracking added %s to the folder", name)
		}
	}
	for _, name := range []string{"skills/x.md", "MEMORY.md"} {
		if after[name] != before[name] {
			t.Errorf("%s changed", name)
		}
	}

	// R-47 (Lane A): leave removes the shadow store and leaves the folder alone.
	folderBefore := hashTree(t, dir)
	if out := mustRun(t, e, dir, nil, "leave"); !strings.Contains(out, "Stopped tracking this folder") {
		t.Fatalf("leave should stop tracking:\n%s", out)
	}
	if ents, _ := os.ReadDir(shadowRoot); len(ents) != 0 {
		t.Errorf("shadow store left behind: %v", ents)
	}
	for name, h := range hashTree(t, dir) {
		if folderBefore[name] != h {
			t.Errorf("leave changed %s", name)
		}
	}
	if _, _, code := e.run(dir, "", "check"); code == 0 {
		t.Error("check after leave should report the folder is not tracked")
	}
}

func TestLaneAOptOutAndGuards(t *testing.T) {
	bin := buildBinary(t)
	e := newCLIEnv(t, bin, "http://127.0.0.1:1")
	dir := filepath.Join(e.home, "notes")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("# A\n"), 0o644)

	mustRun(t, e, dir, []string{"LOOMWORK_NO_TRACK=1"}, "package", "--out", "a.aci")
	if _, err := os.Stat(filepath.Join(e.home, ".loomwork", "folders")); err == nil {
		t.Error("LOOMWORK_NO_TRACK=1 must not create a shadow store")
	}

	// $HOME is never tracked, even with an AGENT.md in it.
	os.WriteFile(filepath.Join(e.home, "AGENT.md"), []byte("# H\n"), 0o644)
	mustRun(t, e, e.home, nil, "package", "--out", "h.aci")
	if _, err := os.Stat(filepath.Join(e.home, ".loomwork", "folders")); err == nil {
		t.Error("home directory must not be tracked")
	}
}
