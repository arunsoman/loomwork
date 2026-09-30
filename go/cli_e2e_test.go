package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type cliEnv struct {
	t    *testing.T
	bin  string
	home string
	url  string
}

func newCLIEnv(t *testing.T, bin, ollamaURL string) *cliEnv {
	return &cliEnv{t: t, bin: bin, home: t.TempDir(), url: ollamaURL}
}

func (e *cliEnv) run(dir string, stdin string, args ...string) (string, string, int) {
	e.t.Helper()
	cmd := exec.Command(e.bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+e.home, "OLLAMA_URL="+e.url, "OLLAMA_MODEL=", "LOOMWORK_MEMORY_PASSPHRASE=")
	cmd.Stdin = strings.NewReader(stdin)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatal(err)
	}
	return so.String(), se.String(), code
}

func fakeOllamaServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			w.Write([]byte(`{"models":[{"name":"llama3.2:latest"}]}`))
		case "/api/chat":
			var req struct {
				Messages []struct{ Content string } `json:"messages"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			last := req.Messages[len(req.Messages)-1].Content
			json.NewEncoder(w).Encode(map[string]interface{}{
				"model": "llama3.2:latest", "done": true, "prompt_eval_count": 3, "eval_count": 2,
				"message": map[string]string{"role": "assistant", "content": "pong: " + strings.SplitN(last, "\n", 2)[0]},
			})
		}
	}))
}

func TestCLIEndToEnd(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "loomwork")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	srv := fakeOllamaServer()
	defer srv.Close()
	e := newCLIEnv(t, bin, srv.URL)
	work := filepath.Join(e.home, "work")
	os.MkdirAll(work, 0o755)

	// init refuses to overwrite; package works and is verifiable
	if _, se, code := e.run(work, "", "init", "agent"); code != 0 {
		t.Fatalf("init: %s", se)
	}
	if _, _, code := e.run(work, "", "init", "agent"); code == 0 {
		t.Fatal("init must not overwrite an existing agent")
	}
	agent := filepath.Join(work, "agent")
	os.WriteFile(filepath.Join(agent, ".env"), []byte("API_KEY=hunter2"), 0o644)
	if _, se, code := e.run(agent, "", "package", "--out", "agent.aci"); code != 0 {
		t.Fatalf("package: %s", se)
	}
	raw, _ := os.ReadFile(filepath.Join(agent, "agent.aci"))
	if bytes.Contains(gunzip(t, raw), []byte("hunter2")) {
		t.Fatal(".env was packed into the ACI")
	}
	if so, se, code := e.run(agent, "", "verify", "agent.aci"); code != 0 || !strings.Contains(so, "ACI verified") {
		t.Fatalf("verify own ACI: code %d\n%s\n%s", code, so, se)
	}

	// run works with flags before and after the path; REPL ends at EOF
	for _, args := range [][]string{{"run", "--input", "hello", "agent.aci"}, {"run", "agent.aci", "--input", "hello"}} {
		so, se, code := e.run(agent, "", args...)
		if code != 0 || !strings.Contains(so, "pong: hello") {
			t.Fatalf("%v: code %d\n%s\n%s", args, code, so, se)
		}
		if !strings.Contains(se, "checks=PASSED") || !strings.Contains(se, "trusted signer") {
			t.Fatalf("receipt missing:\n%s", se)
		}
	}
	if so, _, code := e.run(agent, "line one\n", "run", "agent.aci"); code != 0 || !strings.Contains(so, "pong: line one") {
		t.Fatalf("repl: code %d\n%s", code, so)
	}

	// a forged receipt is not shown as valid
	os.WriteFile(filepath.Join(agent, "agent.aci.attestation.json"),
		[]byte(`{"propertyTests":{"passed":true},"signer":"TRUSTED-BY-ME","signedAt":"2099","signature":"AAAA"}`), 0o644)
	_, se, _ := e.run(agent, "", "run", "--input", "x", "agent.aci")
	if strings.Contains(se, "PASSED") || !strings.Contains(se, "receipt: none") {
		t.Fatalf("forged receipt must be rejected:\n%s", se)
	}
	e.run(agent, "", "package", "--out", "agent.aci") // regenerate the real one

	// unsigned and tampered archives are refused
	unsigned := rewriteTar(t, raw, func(name string, b []byte) ([]byte, bool) { return b, !strings.HasPrefix(name, "signatures/") })
	os.WriteFile(filepath.Join(agent, "unsigned.aci"), unsigned, 0o644)
	if _, _, code := e.run(agent, "", "verify", "unsigned.aci"); code == 0 {
		t.Fatal("verify must fail for an unsigned ACI")
	}
	if _, se, code := e.run(agent, "", "run", "--input", "x", "unsigned.aci"); code == 0 || !strings.Contains(se, "no signature") {
		t.Fatalf("run must refuse an unsigned ACI: %d %s", code, se)
	}
	if so, _, code := e.run(agent, "", "run", "--input", "x", "--allow-unsigned", "unsigned.aci"); code != 0 || !strings.Contains(so, "pong") {
		t.Fatal("--allow-unsigned should override")
	}
	tampered := rewriteTar(t, raw, func(name string, b []byte) ([]byte, bool) {
		if name == "persona/system_prompt.md" {
			return append(b, []byte("\nevil")...), true
		}
		return b, true
	})
	os.WriteFile(filepath.Join(agent, "tampered.aci"), tampered, 0o644)
	if _, _, code := e.run(agent, "", "run", "--input", "x", "tampered.aci"); code == 0 {
		t.Fatal("tampered ACI must be refused")
	}

	// an ACI signed by someone else: valid signature, but untrusted until added
	other := newCLIEnv(t, bin, srv.URL)
	ow := filepath.Join(other.home, "w")
	os.MkdirAll(ow, 0o755)
	other.run(ow, "", "init", "theirs")
	other.run(filepath.Join(ow, "theirs"), "", "package", "--out", filepath.Join(work, "theirs.aci"))
	if _, se, code := e.run(work, "", "run", "--input", "x", "theirs.aci"); code == 0 || !strings.Contains(se, "not trusted") {
		t.Fatalf("an ACI from an unknown signer must be refused: %d %s", code, se)
	}
	if _, _, code := e.run(work, "", "verify", "theirs.aci"); code == 0 {
		t.Fatal("verify must fail for an untrusted signer")
	}
	if so, se, code := e.run(work, "", "trust", "add", "--yes", "theirs.aci"); code != 0 {
		t.Fatalf("trust add: %s %s", so, se)
	}
	if so, se, code := e.run(work, "", "run", "--input", "hi", "theirs.aci"); code != 0 || !strings.Contains(so, "pong") {
		t.Fatalf("run after trusting: %d %s %s", code, so, se)
	}

	// typed memory: encrypted at rest, consent-filtered, revocation deletes
	if _, se, code := e.run(work, "", "memory", "propose", "--kind", "preference", "--allow-agent", "agent",
		"--json", `{"key":"color","value":"MAGENTAUNIQUE","source":"user_stated","confidence":1}`); code != 0 {
		t.Fatalf("propose: %s", se)
	}
	so, _, _ := e.run(work, "", "memory", "list")
	id := strings.Fields(so)[0]
	e.run(work, "", "memory", "approve", id)
	db, _ := os.ReadFile(filepath.Join(e.home, ".loomwork", "memory.db"))
	if bytes.Contains(db, []byte("MAGENTAUNIQUE")) {
		t.Fatal("typed memory stored in plaintext")
	}
	e.run(agent, "", "run", "--input", "what color?", "agent.aci")
	if so, _, code := e.run(work, "", "memory", "revoke", id); code != 0 || !strings.Contains(so, "cannot be undone") {
		t.Fatalf("revoke: %s", so)
	}
	if so, _, _ := e.run(work, "", "memory", "get", id); strings.Contains(so, "MAGENTAUNIQUE") {
		t.Fatal("revoked content still readable")
	}
	db, _ = os.ReadFile(filepath.Join(e.home, ".loomwork", "memory.db"))
	if bytes.Contains(db, []byte("MAGENTAUNIQUE")) {
		t.Fatal("revoked content still in the database file")
	}

	// AMP hand-off between two agents over stdio
	tokenFile := filepath.Join(work, "token.json")
	if _, se, code := e.run(work, "", "amp", "token", "--skill", "answer_question", "--out", tokenFile); code != 0 {
		t.Fatalf("amp token: %s", se)
	}
	callee := filepath.Join(agent, "agent.aci")
	so, se, code := e.run(work, "", "amp", "delegate", "--exec", bin+" amp serve "+callee, "--from", filepath.Join(agent, "agent.aci"),
		"--token", tokenFile, "summarize this")
	if code != 0 || !strings.Contains(so, "pong: summarize this") {
		t.Fatalf("amp delegate: code %d\n%s\n%s", code, so, se)
	}
	// without a token the peer refuses
	os.WriteFile(filepath.Join(work, "empty.json"), []byte(`{"id":"x","skill":"answer_question","issuedBy":"y","issuedAt":"2020-01-01T00:00:00Z","expiresAt":"2020-01-02T00:00:00Z"}`), 0o644)
	if _, se, code := e.run(work, "", "amp", "delegate", "--exec", bin+" amp serve "+callee, "--from", filepath.Join(agent, "agent.aci"),
		"--token", filepath.Join(work, "empty.json"), "x"); code == 0 || !strings.Contains(se, "403") {
		t.Fatalf("expired/unsigned token must be denied: %d %s", code, se)
	}
}

func gunzip(t *testing.T, b []byte) []byte {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(zr)
	return out
}

// rewriteTar copies a .aci, letting fn edit or drop each file.
func rewriteTar(t *testing.T, raw []byte, fn func(name string, data []byte) ([]byte, bool)) []byte {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(gunzip(t, raw)))
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		data, _ := io.ReadAll(tr)
		data, keep := fn(h.Name, data)
		if !keep {
			continue
		}
		tw.WriteHeader(&tar.Header{Name: h.Name, Typeflag: tar.TypeReg, Size: int64(len(data)), Mode: 0o644})
		tw.Write(data)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// A plain folder (AGENT.md, skills/*.md, MEMORY.md) goes through init, package
// and verify without hand-writing any JSON, and the user's files stay as written.
func TestPlainFolderFrontend(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "loomwork")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	srv := fakeOllamaServer()
	defer srv.Close()
	e := newCLIEnv(t, bin, srv.URL)
	dir := filepath.Join(e.home, "notes-agent")
	os.MkdirAll(filepath.Join(dir, "skills"), 0o755)
	agent := "# Notes agent\n\nYou summarise notes.\n"
	skill := "---\nname: summarise\ndescription: Summarise a note\n---\nSteps...\n"
	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte(agent), 0o644)
	os.WriteFile(filepath.Join(dir, "skills", "summarise.md"), []byte(skill), 0o644)
	os.WriteFile(filepath.Join(dir, "skills", "tag.md"), []byte("Tag a note.\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("# Memory\n- prefers short notes\n"), 0o644)

	if so, se, code := e.run(dir, "", "package", "--out", "notes.aci"); code != 0 {
		t.Fatalf("package: %s%s", so, se)
	}
	if so, se, code := e.run(dir, "", "verify", "notes.aci"); code != 0 {
		t.Fatalf("verify: %s%s", so, se)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "AGENT.md")); string(b) != agent {
		t.Fatal("AGENT.md must not be rewritten")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "notes.aci"))
	names := map[string]bool{}
	tr := tar.NewReader(bytes.NewReader(gunzip(t, raw)))
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names[h.Name] = true
	}
	if names["MEMORY.md"] {
		t.Error("MEMORY.md is memory content and must not be packed into the ACI (PRD §8.3)")
	}
	for _, want := range []string{"AGENT.md", "skills/summarise.md", "skills/tag.md", "manifest.json"} {
		if !names[want] {
			t.Errorf("archive is missing %s (have %v)", want, names)
		}
	}
	// Re-packaging must pick up a skill added after the first package, not drop it.
	os.WriteFile(filepath.Join(dir, "skills", "review.md"), []byte("Review a note.\n"), 0o644)
	if so, se, code := e.run(dir, "", "package", "--out", "notes2.aci"); code != 0 {
		t.Fatalf("re-package: %s%s", so, se)
	}
	raw2, _ := os.ReadFile(filepath.Join(dir, "notes2.aci"))
	found := false
	tr2 := tar.NewReader(bytes.NewReader(gunzip(t, raw2)))
	for {
		h, err := tr2.Next()
		if err != nil {
			break
		}
		found = found || h.Name == "skills/review.md"
	}
	if !found {
		t.Error("skill added after init was silently dropped on re-package")
	}
	so, se, code := e.run(dir, "", "run", "--input", "ping", "notes.aci")
	if code != 0 || !strings.Contains(so, "pong") {
		t.Fatalf("run: %s%s", so, se)
	}
}

func TestCheckAgentNameRejectsWindowsDeviceNames(t *testing.T) {
	for _, bad := range []string{"con", "NUL", "com1", "LPT9"} {
		if checkAgentName(bad) == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if err := checkAgentName("my-agent"); err != nil {
		t.Errorf("ordinary name rejected: %v", err)
	}
}
