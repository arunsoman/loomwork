package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "loomwork")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

// hashTree maps every regular file under dir (slash paths) to its sha256.
func hashTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

var proposedID = regexp.MustCompile(`id: (mem_[a-z0-9_]+)`)

func mustRun(t *testing.T, e *cliEnv, dir string, extra []string, args ...string) string {
	t.Helper()
	so, se, code := e.runEnv(dir, extra, "", args...)
	if code != 0 {
		t.Fatalf("loomwork %s: exit %d\n%s%s", strings.Join(args, " "), code, so, se)
	}
	return so + se
}

// R-29: contract rules 1-3 end to end. Loomwork lives beside the user's
// markdown folder and never changes it; after `leave` the folder is
// byte-identical and its own tooling still works.
func TestByteIdenticalLifecycle(t *testing.T) {
	bin := buildBinary(t)
	e := newCLIEnv(t, bin, "http://127.0.0.1:1") // no model needed
	dir := filepath.Join(e.home, "notes")
	os.MkdirAll(filepath.Join(dir, "skills"), 0o755)
	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("# Notes agent\nYou keep notes.\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "skills", "x.md"), []byte("Do x carefully.\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("# Memory\n\n## Coffee\nflat white\n\n## Editor\nvim\n\n## Zone\nUTC\n"), 0o644)
	before := hashTree(t, dir)

	mustRun(t, e, dir, nil, "package", "--out", "demo.aci")
	if out := mustRun(t, e, dir, nil, "memory", "import", "MEMORY.md"); strings.Count(out, "mem_") < 3 {
		t.Fatalf("import should create 3 records:\n%s", out)
	}
	var ids []string
	for i, claim := range []string{"APPROVED-BELIEF-ONE", "second, gated", "third stays pending"} {
		extra := []string(nil)
		if i == 1 {
			extra = []string{"LOOMWORK_WRITE_GATE=1"}
		}
		out := mustRun(t, e, dir, extra, "memory", "propose", "--kind", "belief", "--as-agent", "bot",
			"--json", `{"claim":"`+claim+`","confidence":0.7}`)
		m := proposedID.FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("no id in %q", out)
		}
		ids = append(ids, m[1])
	}
	mustRun(t, e, dir, nil, "memory", "approve", ids[0])
	mustRun(t, e, dir, nil, "memory", "export", "--out", "out.md")
	if out := mustRun(t, e, dir, nil, "leave", "--export", "final.md", "--delete-store"); !strings.Contains(out, "Exported 1 active") {
		t.Fatalf("leave output:\n%s", out)
	}

	// Rule 1 and 3: every user file is byte-identical, and nothing else was
	// added to the folder beyond the archive, its sidecars and the exports.
	after := hashTree(t, dir)
	allowedNew := map[string]bool{"demo.aci": true, "demo.slsa.json": true, "demo.aci.attestation.json": true, "out.md": true, "final.md": true}
	for name, h := range before {
		if after[name] != h {
			t.Errorf("user file %s changed or vanished", name)
		}
	}
	var added []string
	for name := range after {
		if _, ok := before[name]; !ok {
			added = append(added, name)
			if !allowedNew[name] {
				t.Errorf("unexpected new file in the folder: %s", name)
			}
		}
	}
	sort.Strings(added)
	t.Logf("files added to the folder: %v", added)

	// Rule 4: exports hold the approved record and only it.
	for _, f := range []string{"out.md", "final.md"} {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		if !strings.Contains(string(b), "APPROVED-BELIEF-ONE") || strings.Contains(string(b), "third stays pending") || strings.Contains(string(b), "flat white") {
			t.Errorf("%s should hold exactly the approved record:\n%s", f, b)
		}
	}

	// Rule 2 and 3: the store is gone, the signing key is not, and the archive still verifies.
	for _, f := range []string{"memory.db", "memory.key", "memory.db.salt"} {
		if _, err := os.Stat(filepath.Join(e.home, ".loomwork", f)); err == nil {
			t.Errorf("%s should have been deleted", f)
		}
	}
	if _, err := os.Stat(filepath.Join(e.home, ".loomwork", "key.pem")); err != nil {
		t.Error("leave must not delete the signing key")
	}
	mustRun(t, e, dir, nil, "verify", "demo.aci")
}

func TestLeaveDeleteStoreNeedsExportOrYes(t *testing.T) {
	e := newCLIEnv(t, buildBinary(t), "http://127.0.0.1:1")
	dir := t.TempDir()
	mustRun(t, e, dir, nil, "memory", "propose", "--kind", "belief", "--json", `{"claim":"keep me"}`)
	db := filepath.Join(e.home, ".loomwork", "memory.db")
	if out := mustRun(t, e, dir, nil, "leave", "--delete-store"); !strings.Contains(out, "Nothing was deleted") {
		t.Fatalf("guidance expected:\n%s", out)
	}
	if _, err := os.Stat(db); err != nil {
		t.Fatal("--delete-store alone must be a no-op")
	}
	if _, se, code := e.run(dir, "", "leave", "--export", "MEMORY.md", "--delete-store"); code == 0 || !strings.Contains(se, "MEMORY.md") {
		t.Fatalf("export to MEMORY.md must be refused (exit %d): %s", code, se)
	}
	if _, err := os.Stat(db); err != nil {
		t.Fatal("a refused export must not delete the store")
	}
	if _, err := os.Stat(filepath.Join(dir, "MEMORY.md")); err == nil {
		t.Fatal("MEMORY.md must never be created by export")
	}
	mustRun(t, e, dir, nil, "leave", "--delete-store", "--yes")
	if _, err := os.Stat(db); err == nil {
		t.Fatal("--delete-store --yes should delete the store")
	}
}

func TestExportRefusesOverwriteAndMemoryMD(t *testing.T) {
	e := newCLIEnv(t, buildBinary(t), "http://127.0.0.1:1")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("mine\n"), 0o644)
	for _, args := range [][]string{
		{"memory", "export", "--out", "MEMORY.md"},
		{"memory", "export", "--out", "MEMORY.md", "--force"},
		{"memory", "export", "--out", "sub/memory.md"}, // parent missing is an error, not a silent mkdir
	} {
		if _, _, code := e.run(dir, "", args...); code == 0 {
			t.Errorf("%v should fail", args)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "MEMORY.md")); string(b) != "mine\n" {
		t.Fatal("MEMORY.md was modified")
	}
	mustRun(t, e, dir, nil, "memory", "export", "--out", "memory-export.md")
	if _, _, code := e.run(dir, "", "memory", "export", "--out", "memory-export.md"); code == 0 {
		t.Error("second export must not overwrite without --force")
	}
	mustRun(t, e, dir, nil, "memory", "export", "--out", "memory-export.md", "--force")
}

func TestImportLeavesSourceAloneAndRefusesSymlink(t *testing.T) {
	e := newCLIEnv(t, buildBinary(t), "http://127.0.0.1:1")
	dir := t.TempDir()
	src := filepath.Join(dir, "MEMORY.md")
	os.WriteFile(src, []byte("## A\none\n\n## B\ntwo\n\n## C\nthree\n"), 0o644)
	before := hashTree(t, dir)
	mustRun(t, e, dir, nil, "memory", "import", "MEMORY.md")
	if out := mustRun(t, e, dir, nil, "memory", "list", "--status", "active"); strings.Contains(out, "mem_") {
		t.Fatalf("import must not activate records:\n%s", out)
	}
	if out := mustRun(t, e, dir, nil, "memory", "stats"); !strings.Contains(out, "pending: 3") {
		t.Fatalf("want 3 pending:\n%s", out)
	}
	if after := hashTree(t, dir); len(after) != 1 || after["MEMORY.md"] != before["MEMORY.md"] {
		t.Fatal("import changed the folder")
	}
	os.Symlink(src, filepath.Join(dir, "link.md"))
	if _, _, code := e.run(dir, "", "memory", "import", "link.md"); code == 0 {
		t.Error("symlink import must be refused")
	}
}

// WI-3: a native-mode folder warns about files the manifest does not cover;
// a plain (coexistence) folder re-derives everything and has nothing to warn about.
func TestUnlistedFileWarning(t *testing.T) {
	e := newCLIEnv(t, buildBinary(t), "http://127.0.0.1:1")
	mk := func(name string) string {
		dir := filepath.Join(e.home, name)
		os.MkdirAll(filepath.Join(dir, "skills"), 0o755)
		os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("# A\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "skills", "a.md"), []byte("Skill.\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("m\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "notes.md"), []byte("stray\n"), 0o644)
		os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
		os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("x"), 0o644)
		return dir
	}
	native := mk("native")
	mustRun(t, e, native, nil, "init")
	so, se, code := e.run(native, "", "package", "--out", "n.aci")
	if code != 0 {
		t.Fatalf("package must still succeed: %s%s", so, se)
	}
	if !strings.Contains(se, "notes.md") || strings.Contains(se, "MEMORY.md") || strings.Contains(se, ".git") || strings.Contains(se, "skills/a.md") {
		t.Fatalf("warning should name notes.md only:\n%s", se)
	}
	mustRun(t, e, native, nil, "verify", "n.aci")

	// Cap: more than 10 strays -> "and N more".
	for i := 0; i < 12; i++ {
		os.WriteFile(filepath.Join(native, "s"+string(rune('a'+i))+".txt"), []byte("x"), 0o644)
	}
	if _, se, _ := e.run(native, "", "package", "--out", "n2.aci"); !strings.Contains(se, "and 3 more") {
		t.Fatalf("expected the list to be capped at 10:\n%s", se)
	}

	plain := mk("plain")
	if _, se, code := e.run(plain, "", "package", "--out", "p.aci"); code != 0 || strings.Contains(se, "NOT be packed") {
		t.Fatalf("plain folder should not warn (exit %d): %s", code, se)
	}
}

// WI-2: in a plain folder, package writes only the archive and its sidecars,
// and a skill added afterwards is packed without any init or manifest step.
func TestPlainPackageIsZeroFootprintAndNeverStale(t *testing.T) {
	e := newCLIEnv(t, buildBinary(t), "http://127.0.0.1:1")
	dir := filepath.Join(e.home, "plain")
	os.MkdirAll(filepath.Join(dir, "skills"), 0o755)
	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("# A\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "skills", "research.md"), []byte("Research.\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("CANARY-MEMORY\n"), 0o644)
	before := hashTree(t, dir)

	mustRun(t, e, dir, nil, "package", "--out", "demo.aci")
	after := hashTree(t, dir)
	for name := range after {
		if _, ok := before[name]; ok {
			if after[name] != before[name] {
				t.Errorf("%s changed", name)
			}
			continue
		}
		if name != "demo.aci" && name != "demo.slsa.json" && name != "demo.aci.attestation.json" {
			t.Errorf("package wrote %s into the folder", name)
		}
	}
	names := tarNames(t, filepath.Join(dir, "demo.aci"))
	if names["skills/review.md"] || names["MEMORY.md"] || !names["skills/research.md"] || !names["AGENT.md"] {
		t.Fatalf("unexpected archive entries: %v", names)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "demo.aci"))
	if bytes.Contains(gunzip(t, raw), []byte("CANARY-MEMORY")) {
		t.Fatal("MEMORY.md content leaked into the archive")
	}
	mustRun(t, e, dir, nil, "verify", "demo.aci")

	os.WriteFile(filepath.Join(dir, "skills", "review.md"), []byte("Review.\n"), 0o644)
	mustRun(t, e, dir, nil, "package", "--out", "demo2.aci")
	if !tarNames(t, filepath.Join(dir, "demo2.aci"))["skills/review.md"] {
		t.Fatal("a skill added after the first package was not packed (stale manifest)")
	}
}

func tarNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	tr := tar.NewReader(bytes.NewReader(gunzip(t, raw)))
	for {
		h, err := tr.Next()
		if err != nil {
			return names
		}
		names[h.Name] = true
	}
}

// WI-7: a folder-built ACI, run through the real binary, sends its markdown
// skill text to the model. The fake model records the system prompt it receives.
func TestFolderACIRunSendsSkillToModel(t *testing.T) {
	var mu sync.Mutex
	var systems []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			w.Write([]byte(`{"models":[{"name":"llama3.2:latest"}]}`))
		case "/api/chat":
			var req struct {
				Messages []struct{ Role, Content string } `json:"messages"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			systems = append(systems, req.Messages[0].Content)
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]interface{}{"model": "llama3.2:latest", "done": true,
				"message": map[string]string{"role": "assistant", "content": "ok"}})
		}
	}))
	defer srv.Close()
	e := newCLIEnv(t, buildBinary(t), srv.URL)
	dir := filepath.Join(e.home, "agent")
	os.MkdirAll(filepath.Join(dir, "skills"), 0o755)
	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("You are the folder agent.\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "skills", "research.md"), []byte("Cite sources. MARKER-PHRASE-7Q\n"), 0o644)
	mustRun(t, e, dir, nil, "package", "--out", "agent.aci")
	mustRun(t, e, dir, nil, "run", "--input", "hello", "agent.aci")
	mu.Lock()
	defer mu.Unlock()
	if len(systems) == 0 || !strings.Contains(systems[0], "MARKER-PHRASE-7Q") || !strings.Contains(systems[0], "You are the folder agent.") {
		t.Fatalf("skill text did not reach the model; system prompt was: %q", systems)
	}
}
