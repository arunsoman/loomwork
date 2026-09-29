package llm

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serverWith(models ...string) *Ollama {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var items []string
		for _, m := range models {
			items = append(items, `{"name":"`+m+`"}`)
		}
		w.Write([]byte(`{"models":[` + strings.Join(items, ",") + `]}`))
	}))
	return NewOllama(srv.URL)
}

func TestResolveModel(t *testing.T) {
	t.Setenv("OLLAMA_MODEL", "")
	pref := []string{"llama3.2", "qwen2.5"}
	cases := []struct {
		name      string
		installed []string
		want      string
		notice    string // substring; "" = none expected
	}{
		{"preferred tag match", []string{"llama3.2:latest"}, "llama3.2:latest", ""},
		{"second preference", []string{"qwen2.5:7b"}, "qwen2.5:7b", "not installed"},
		{"local beats cloud", []string{"glm-5.1:cloud", "lfm2.5:latest"}, "lfm2.5:latest", "local model"},
		{"cloud only", []string{"gpt-oss:120b-cloud"}, "gpt-oss:120b-cloud", "cloud model"},
	}
	for _, c := range cases {
		got, notice, err := serverWith(c.installed...).ResolveModel(pref)
		if err != nil || got != c.want {
			t.Errorf("%s: got %q err %v, want %q", c.name, got, err, c.want)
		}
		if (c.notice == "") != (notice == "") || !strings.Contains(notice, c.notice) {
			t.Errorf("%s: notice %q, want containing %q", c.name, notice, c.notice)
		}
	}
	if _, _, err := serverWith().ResolveModel(pref); err == nil {
		t.Error("expected error when nothing installed")
	}
}

func TestChatFallbackErrorAndOrder(t *testing.T) {
	t.Setenv("OLLAMA_MODEL", "")
	c, err := serverWith("glm-5.1:cloud", "gpt-oss:120b-cloud", "lfm2.5:latest").ResolveModels([]string{"llama3.2"})
	if err != nil || len(c) != 3 || c[0].Name != "lfm2.5:latest" || c[1].Name != "glm-5.1:cloud" {
		t.Fatalf("unexpected order: %v %v", c, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
		w.Write([]byte(`{"error":"retired"}`))
	}))
	_, err = NewOllama(srv.URL).Chat(&ChatRequest{Model: "glm-5.1:cloud"})
	var mu *ModelUnavailableError
	if !errors.As(err, &mu) || mu.Status != 410 {
		t.Fatalf("want ModelUnavailableError 410, got %v", err)
	}
}
