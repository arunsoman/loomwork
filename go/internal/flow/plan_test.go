package flow

import (
	"strings"
	"testing"
)

func TestParsePlan(t *testing.T) {
	ok := `Here you go: {"modules":[{"id":"api","spec":"s","paths":["api"]},{"id":"ui","spec":"s","depends_on":["api"],"paths":["ui"]}]}`
	ms, err := ParsePlan(ok)
	if err != nil || len(ms) != 2 {
		t.Fatalf("%v %v", ms, err)
	}
	bad := map[string]string{
		"cycle":   `{"modules":[{"id":"a","spec":"s","depends_on":["b"]},{"id":"b","spec":"s","depends_on":["a"]}]}`,
		"unknown": `{"modules":[{"id":"a","spec":"s","depends_on":["zz"]}]}`,
		"overlap": `{"modules":[{"id":"a","spec":"s","paths":["src"]},{"id":"b","spec":"s","paths":["src/x"]}]}`,
		"unsafe":  `{"modules":[{"id":"../a","spec":"s"}]}`,
		"escape":  `{"modules":[{"id":"a","spec":"s","paths":["../x"]}]}`,
		"empty":   `{"modules":[]}`,
		"nojson":  `sorry`,
		"self":    `{"modules":[{"id":"a","spec":"s","depends_on":["a"]}]}`,
	}
	for name, in := range bad {
		if _, err := ParsePlan(in); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestVerdictFailsClosed(t *testing.T) {
	if v := ParseVerdict("pi", "looks great!"); v.Pass {
		t.Fatal("prose must not pass")
	}
	if v := ParseVerdict("pi", `{"issues":[]}`); v.Pass {
		t.Fatal("missing pass field must not pass")
	}
	if v := ParseVerdict("pi", `ok {"pass":true,"issues":[]}`); !v.Pass {
		t.Fatal("explicit pass should pass")
	}
}

func TestWorkflowValidate(t *testing.T) {
	if err := Default().Validate(nil); err != nil {
		t.Fatal(err)
	}
	w := Default()
	w.Stages[2].Agents = []string{"claude"}
	if err := w.Validate(nil); err == nil || !strings.Contains(err.Error(), "different agent") {
		t.Fatalf("builder==verifier accepted: %v", err)
	}
	w = Default()
	w.Stages = append(w.Stages[:2], w.Stages[3])
	if err := w.Validate(nil); err == nil {
		t.Fatal("ship without verify accepted")
	}
	w = Default()
	w.Stages[0], w.Stages[1] = w.Stages[1], w.Stages[0]
	if err := w.Validate(nil); err == nil {
		t.Fatal("wrong order accepted")
	}
	if err := Default().Validate(map[string]bool{"codex": true}); err == nil {
		t.Fatal("unknown agent accepted")
	}
}

func TestBoardClaimRace(t *testing.T) {
	b, _ := OpenBoard(t.TempDir())
	wins := make(chan string, 20)
	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func(i int) {
			if b.Claim("t1", string(rune('a'+i)), 5e9) == nil {
				wins <- "x"
			}
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 20; i++ {
		<-done
	}
	if len(wins) != 1 {
		t.Fatalf("%d winners", len(wins))
	}
	b.Release("t1", "wrong")
	if b.Claim("t1", "z", 5e9) == nil {
		t.Fatal("release by non-owner freed the claim")
	}
}

func TestReady(t *testing.T) {
	b, _ := OpenBoard(t.TempDir())
	b.Put(&Task{ID: "a", Status: StatMerged})
	b.Put(&Task{ID: "b", Status: StatPlanned, DependsOn: []string{"a"}})
	b.Put(&Task{ID: "c", Status: StatPlanned, DependsOn: []string{"b"}})
	r, _ := b.Ready()
	if len(r) != 1 || r[0].ID != "b" {
		t.Fatalf("%v", r)
	}
}
