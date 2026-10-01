package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Task statuses.
const (
	StatPlanned   = "planned"
	StatBuilding  = "building"
	StatBuilt     = "built"
	StatVerifying = "verifying"
	StatVerified  = "verified"
	StatMerged    = "merged"
	StatFailed    = "failed"
)

// Verdict is one verifier's judgement.
type Verdict struct {
	Agent  string   `json:"agent"`
	Pass   bool     `json:"pass"`
	Issues []string `json:"issues,omitempty"`
}

// Event is one line of a task's history: who did what, when.
type Event struct {
	Time   time.Time `json:"time"`
	Agent  string    `json:"agent"`
	Action string    `json:"action"`
	Note   string    `json:"note,omitempty"`
}

// Task is one module of the plan.
type Task struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Spec      string    `json:"spec"`
	DependsOn []string  `json:"depends_on,omitempty"`
	Paths     []string  `json:"paths,omitempty"` // path prefixes the builder may touch
	Status    string    `json:"status"`
	Owner     string    `json:"owner,omitempty"` // agent currently working on it
	Attempts  int       `json:"attempts"`
	Branch    string    `json:"branch,omitempty"`
	Worktree  string    `json:"worktree,omitempty"`
	Verdicts  []Verdict `json:"verdicts,omitempty"`
	Files     []string  `json:"files,omitempty"`    // files the builder changed
	TestLog   string    `json:"test_log,omitempty"` // tail of the last test command output
	Events    []Event   `json:"events,omitempty"`
	Updated   time.Time `json:"updated"`
}

// Board stores tasks as one JSON file each under <folder>/.agent/state/tasks.
// Files are written atomically and claims use O_EXCL lock files, so several
// Loom processes (or the status TUI) can share a board safely.
type Board struct{ dir string }

// OpenBoard opens (creating if needed) the board for a project folder.
func OpenBoard(folder string) (*Board, error) {
	dir := filepath.Join(folder, ".agent", "state", "tasks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Board{dir: dir}, nil
}

func (b *Board) path(id string) string { return filepath.Join(b.dir, id+".json") }

// Put saves a task atomically.
func (b *Board) Put(t *Task) error {
	if !nameRe.MatchString(t.ID) {
		return fmt.Errorf("invalid task id %q", t.ID)
	}
	t.Updated = time.Now().UTC()
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(b.path(t.ID), append(data, '\n'))
}

// Get loads one task.
func (b *Board) Get(id string) (*Task, error) {
	if !nameRe.MatchString(id) {
		return nil, fmt.Errorf("invalid task id %q", id)
	}
	data, err := os.ReadFile(b.path(id))
	if err != nil {
		return nil, err
	}
	var t Task
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("task %s: %w", id, err)
	}
	return &t, nil
}

// List returns all tasks sorted by id.
func (b *Board) List() ([]*Task, error) {
	ents, err := os.ReadDir(b.dir)
	if err != nil {
		return nil, err
	}
	var out []*Task
	for _, e := range ents {
		n := e.Name()
		if filepath.Ext(n) != ".json" {
			continue
		}
		t, err := b.Get(n[:len(n)-5])
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Clear removes every task (a new plan replaces the old board).
func (b *Board) Clear() error {
	ents, err := os.ReadDir(b.dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if err := os.Remove(filepath.Join(b.dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// ErrClaimed means another worker holds a live claim.
var ErrClaimed = errors.New("task is claimed by another worker")

type claim struct {
	Owner   string    `json:"owner"`
	Expires time.Time `json:"expires"`
}

// Claim takes an exclusive lease on a task. An expired lease is taken over.
// Call Release when done; Renew to extend while working.
func (b *Board) Claim(id, owner string, lease time.Duration) error {
	lock := filepath.Join(b.dir, id+".lock")
	body, err := json.Marshal(claim{Owner: owner, Expires: time.Now().Add(lease)})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(b.dir, ".claim-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		// A hard link is created atomically with full contents and fails if
		// the lock exists, so a reader never sees a half-written claim.
		err := os.Link(tmp.Name(), lock)
		if err == nil {
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		data, rerr := os.ReadFile(lock)
		var c claim
		if rerr == nil && json.Unmarshal(data, &c) == nil && time.Now().Before(c.Expires) {
			return fmt.Errorf("%w: %s until %s", ErrClaimed, c.Owner, c.Expires.Format(time.RFC3339))
		}
		// Expired: remove only if it is still the lease we read. A narrow
		// race remains between that check and the remove, and only after a
		// lease has already expired.
		if again, e := os.ReadFile(lock); e == nil && string(again) == string(data) {
			os.Remove(lock)
		}
	}
	return ErrClaimed
}

// Release drops a claim held by owner.
func (b *Board) Release(id, owner string) {
	lock := filepath.Join(b.dir, id+".lock")
	if data, err := os.ReadFile(lock); err == nil {
		var c claim
		if json.Unmarshal(data, &c) == nil && c.Owner == owner {
			os.Remove(lock)
		}
	}
}

// Ready returns planned tasks whose dependencies are all merged and which
// have no live claim.
func (b *Board) Ready() ([]*Task, error) {
	all, err := b.List()
	if err != nil {
		return nil, err
	}
	state := map[string]string{}
	for _, t := range all {
		state[t.ID] = t.Status
	}
	var out []*Task
	for _, t := range all {
		if t.Status != StatPlanned {
			continue
		}
		ok := true
		for _, d := range t.DependsOn {
			if state[d] != StatMerged {
				ok = false
			}
		}
		if ok {
			out = append(out, t)
		}
	}
	return out, nil
}

// Log appends an event and saves.
func (t *Task) Log(agent, action, note string) {
	t.Events = append(t.Events, Event{Time: time.Now().UTC(), Agent: agent, Action: action, Note: note})
}
