package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loomwork.dev/loomwork/internal/llm"
)

func fakeAgent(t *testing.T, body string) Spec {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return Spec{Name: "fake", Cmd: []string{p}, Stdin: true, Probe: []string{p, "--version"}}
}

func TestRunPassesPromptOnStdin(t *testing.T) {
	a := Adapter{Spec: fakeAgent(t, "echo got:$(cat)")}
	res, err := a.Run(context.Background(), Job{Prompt: "hello"})
	if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != "got:hello" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestRunReportsExitCodeAndTimeout(t *testing.T) {
	a := Adapter{Spec: fakeAgent(t, "echo bad >&2; exit 3")}
	res, err := a.Run(context.Background(), Job{Prompt: "x"})
	if err != nil || res.ExitCode != 3 || !strings.Contains(res.Stderr, "bad") {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	slow := Adapter{Spec: fakeAgent(t, "sleep 5")}
	if _, err := slow.Run(context.Background(), Job{Prompt: "x", Timeout: 100 * time.Millisecond}); err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestEnvDropsPassphrase(t *testing.T) {
	t.Setenv("LOOMWORK_MEMORY_PASSPHRASE", "secret")
	a := Adapter{Spec: fakeAgent(t, "echo pass=[$LOOMWORK_MEMORY_PASSPHRASE]")}
	res, _ := a.Run(context.Background(), Job{Prompt: "x"})
	if strings.Contains(res.Stdout, "secret") {
		t.Fatalf("passphrase leaked: %q", res.Stdout)
	}
}

func TestDriverAndPick(t *testing.T) {
	spec := fakeAgent(t, "cat >/dev/null; echo hi from fake")
	cfg := &Config{Chain: []string{"ollama", "fake"}, Agents: []Spec{spec}}
	d, name, err := Pick(context.Background(), cfg, false, "")
	if err != nil || name != "fake" {
		t.Fatalf("pick: %v %v", name, err)
	}
	resp, err := d.ChatContext(context.Background(), &llm.ChatRequest{Messages: []llm.ChatMessage{{Role: "user", Content: "hi"}}})
	if err != nil || resp.Message.Content != "hi from fake" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if d, name, _ := Pick(context.Background(), cfg, true, ""); d != nil || name != "ollama" {
		t.Fatal("ollama up should win")
	}
	if _, _, err := Pick(context.Background(), cfg, false, "missing"); err == nil {
		t.Fatal("forcing an unknown agent must fail")
	}
	bad := &Config{Chain: []string{"ollama"}}
	if _, _, err := Pick(context.Background(), bad, false, ""); err == nil {
		t.Fatal("no driver should error")
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(ConfigPath(home), []byte(`{"chain":["codex"],"agents":[{"name":"codex","cmd":["mycodex"]},{"name":"extra","cmd":["x"]}]}`), 0o644)
	cfg, err := LoadConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := cfg.Spec("codex"); s.Cmd[0] != "mycodex" {
		t.Fatalf("override lost: %+v", s)
	}
	if _, ok := cfg.Spec("extra"); !ok || len(cfg.Chain) != 1 {
		t.Fatal("extra or chain wrong")
	}
}
