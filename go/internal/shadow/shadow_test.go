package shadow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkFolder(t *testing.T) (home, dir string) {
	t.Helper()
	home = t.TempDir()
	dir = filepath.Join(home, "agent")
	os.MkdirAll(filepath.Join(dir, "skills"), 0o755)
	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("persona\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "skills", "a.md"), []byte("skill a\n"), 0o644)
	return
}

func open(t *testing.T, home, dir string) *Store {
	t.Helper()
	s, err := Open(home, dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGuards(t *testing.T) {
	home, dir := mkFolder(t)
	if _, err := Open(home, home); !errors.Is(err, ErrNotApplicable) {
		t.Errorf("home must be refused, got %v", err)
	}
	if _, err := Open(home, "/"); !errors.Is(err, ErrNotApplicable) {
		t.Errorf("/ must be refused, got %v", err)
	}
	empty := filepath.Join(home, "empty")
	os.Mkdir(empty, 0o755)
	if _, err := Open(home, empty); !errors.Is(err, ErrNotApplicable) {
		t.Errorf("folder without AGENT.md must be refused, got %v", err)
	}
	// A symlinked AGENT.md is not followed.
	link := filepath.Join(home, "linked")
	os.Mkdir(link, 0o755)
	os.Symlink(filepath.Join(dir, "AGENT.md"), filepath.Join(link, "AGENT.md"))
	if _, err := Open(home, link); !errors.Is(err, ErrNotApplicable) {
		t.Errorf("symlinked AGENT.md must be refused, got %v", err)
	}
}

func TestTooManyFilesRefused(t *testing.T) {
	home, dir := mkFolder(t)
	for i := 0; i < maxFiles+1; i++ {
		os.WriteFile(filepath.Join(dir, "f"+strings.Repeat("x", 1)+string(rune('a'+i%26))+itoa(i)), nil, 0o644)
	}
	if _, err := open(t, home, dir).Track(); !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("want ErrNotApplicable for a huge tree, got %v", err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

func TestTrackNeverWritesToFolder(t *testing.T) {
	home, dir := mkFolder(t)
	before := snapshotDigest(mustScan(t, dir))
	s := open(t, home, dir)
	res, err := s.Track()
	if err != nil || !res.First {
		t.Fatalf("first track: %v %+v", err, res)
	}
	if res, _ := s.Track(); res.First || len(res.Changes) != 0 {
		t.Errorf("second track should be quiet: %+v", res)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "AGENT.md" && e.Name() != "skills" {
			t.Errorf("unexpected entry in folder: %s", e.Name())
		}
	}
	if snapshotDigest(mustScan(t, dir)) != before {
		t.Error("folder content changed")
	}
	if !strings.HasPrefix(s.Dir, filepath.Join(home, ".loomwork", "folders")) {
		t.Errorf("store outside ~/.loomwork/folders: %s", s.Dir)
	}
}

func mustScan(t *testing.T, dir string) map[string]string {
	t.Helper()
	m, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Recording a change is not acknowledging it.
func TestDetectedIsNotSealed(t *testing.T) {
	home, dir := mkFolder(t)
	s := open(t, home, dir)
	s.Track()
	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("tampered\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "skills", "b.md"), []byte("new\n"), 0o644)
	os.Remove(filepath.Join(dir, "skills", "a.md"))

	res, _ := s.Track()
	if len(res.Changes) != 3 {
		t.Fatalf("want 3 changes, got %+v", res.Changes)
	}
	s.Track() // recorded once, not again
	entries, _ := s.Entries()
	detected := 0
	for _, e := range entries {
		if e.Source == SourceDetected {
			detected++
		}
	}
	if detected != 3 {
		t.Errorf("want 3 detected entries, got %d", detected)
	}
	rep, err := s.Check()
	if err != nil || rep.OK() || len(rep.Drift) != 3 {
		t.Fatalf("check must still report drift after recording: %v %+v", err, rep)
	}
	if _, err := s.Seal(); err != nil {
		t.Fatal(err)
	}
	if rep, _ := s.Check(); !rep.OK() {
		t.Errorf("check should pass after seal: %+v", rep)
	}
}

func TestJournalTamperDetected(t *testing.T) {
	home, dir := mkFolder(t)
	s := open(t, home, dir)
	s.Track()
	for i := 0; i < 3; i++ {
		os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("v"+itoa(i)+"\n"), 0o644)
		s.Track()
	}
	s.Seal()
	jp := filepath.Join(s.Dir, journal)
	orig, _ := os.ReadFile(jp)
	lines := strings.Split(strings.TrimSpace(string(orig)), "\n")

	// Edit a middle line: the chain breaks there.
	edited := append([]string{}, lines...)
	edited[1] = strings.Replace(edited[1], "detected", "user", 1)
	os.WriteFile(jp, []byte(strings.Join(edited, "\n")+"\n"), 0o600)
	if rep, _ := s.Check(); rep.ChainBroken != 3 {
		t.Errorf("want chain broken at line 3, got %+v", rep)
	}

	// Drop the last line: the head no longer matches.
	os.WriteFile(jp, []byte(strings.Join(lines[:len(lines)-1], "\n")+"\n"), 0o600)
	if rep, _ := s.Check(); !rep.HeadMismatch {
		t.Errorf("truncation must be detected: %+v", rep)
	}
}

func TestRemoveDeletesOnlyShadow(t *testing.T) {
	home, dir := mkFolder(t)
	s := open(t, home, dir)
	s.Track()
	os.WriteFile(filepath.Join(home, ".loomwork", "memory.db"), []byte("keep"), 0o600)
	if ok, err := s.Remove(); !ok || err != nil {
		t.Fatalf("remove: %v %v", ok, err)
	}
	if s.Tracked() {
		t.Error("still tracked")
	}
	if _, err := os.Stat(filepath.Join(home, ".loomwork", "memory.db")); err != nil {
		t.Error("remove touched other state")
	}
	if ok, _ := s.Remove(); ok {
		t.Error("second remove should report nothing removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENT.md")); err != nil {
		t.Error("folder was touched")
	}
}
