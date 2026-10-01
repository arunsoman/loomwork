// Package shadow is Lane A of PRD §6.7.1: Loomwork's own record of a user's
// agent folder, kept under ~/.loomwork/folders/<id>/ so that nothing is ever
// written into the folder itself.
//
// The shadow store holds a digest snapshot of the folder, a hash-chained
// journal of changes noticed between commands, and the last snapshot the user
// acknowledged with `seal`. Recording a change never acknowledges it: Check
// compares the folder with the sealed snapshot, not with the last-seen one.
//
// The folder is only read. Removing the shadow store (Remove) is the whole of
// leaving.
package shadow

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	maxFiles = 2000 // a folder bigger than this is a project tree, not an agent
	stateFn  = "state.json"
	journal  = "journal.jsonl"
)

// ErrNotApplicable means the folder is not one Lane A tracks (no AGENT.md,
// $HOME, the filesystem root, a symlinked AGENT.md). Callers skip silently.
var ErrNotApplicable = errors.New("not a trackable agent folder")

// Journal sources.
const (
	SourceBaseline = "baseline" // first sight of the folder
	SourceDetected = "detected" // change found outside Loomwork
	SourceUser     = "user"     // `seal`
)

// Entry is one journal line. Previous is the hex sha256 of the previous line's
// bytes (empty for the first line), which chains the log.
type Entry struct {
	Time     string `json:"time"`
	Source   string `json:"source"`
	Op       string `json:"op"` // baseline | added | changed | removed | seal
	File     string `json:"file,omitempty"`
	Digest   string `json:"digest,omitempty"`
	Previous string `json:"previous"`
}

type state struct {
	Folder  string            `json:"folder"`
	Created string            `json:"created"`
	Sealed  map[string]string `json:"sealed"`
	Seen    map[string]string `json:"seen"`
	Head    string            `json:"head"` // digest of the last journal line
}

// Change is a difference between two snapshots.
type Change struct {
	Op   string // added | changed | removed
	File string
}

// Store is the shadow record of one folder.
type Store struct {
	Dir    string // ~/.loomwork/folders/<id>
	Folder string // resolved absolute path of the tracked folder
	root   string // ~/.loomwork/folders
}

// Open returns the store for folder without creating anything.
func Open(home, folder string) (*Store, error) {
	abs, err := filepath.Abs(folder)
	if err != nil {
		return nil, err
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	if err := eligible(home, abs); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(abs))
	root := filepath.Join(home, ".loomwork", "folders")
	return &Store{Dir: filepath.Join(root, hex.EncodeToString(sum[:8])), Folder: abs, root: root}, nil
}

func eligible(home, abs string) error {
	if abs == string(filepath.Separator) || abs == filepath.Clean(home) {
		return ErrNotApplicable
	}
	if r, err := filepath.EvalSymlinks(home); err == nil && abs == r {
		return ErrNotApplicable
	}
	info, err := os.Lstat(filepath.Join(abs, "AGENT.md"))
	if err != nil || !info.Mode().IsRegular() {
		return ErrNotApplicable
	}
	return nil
}

// Tracked reports whether a shadow record exists.
func (s *Store) Tracked() bool {
	_, err := os.Stat(filepath.Join(s.Dir, stateFn))
	return err == nil
}

// Result describes what Track found.
type Result struct {
	First   bool     // the folder was not tracked before
	Changes []Change // changes recorded since the last command
}

// Track records the folder's current state. The first time it takes a
// baseline and treats it as sealed; afterwards it journals changes made since
// the last command as `detected`. It never modifies the folder.
func (s *Store) Track() (*Result, error) {
	snap, err := Scan(s.Folder)
	if err != nil {
		return nil, err
	}
	st, err := s.load()
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(s.Dir, 0o700); err != nil {
			return nil, err
		}
		st = &state{Folder: s.Folder, Created: now(), Sealed: snap, Seen: snap}
		if err := s.append(st, Entry{Source: SourceBaseline, Op: "baseline", Digest: snapshotDigest(snap)}); err != nil {
			return nil, err
		}
		return &Result{First: true}, s.save(st)
	}
	if err != nil {
		return nil, err
	}
	changes := Diff(st.Seen, snap)
	for _, c := range changes {
		if err := s.append(st, Entry{Source: SourceDetected, Op: c.Op, File: c.File, Digest: snap[c.File]}); err != nil {
			return nil, err
		}
	}
	st.Seen = snap
	if len(changes) > 0 {
		if err := s.save(st); err != nil {
			return nil, err
		}
	}
	return &Result{Changes: changes}, nil
}

// Report is the outcome of Check.
type Report struct {
	Drift        []Change // folder vs the last sealed snapshot
	ChainBroken  int      // 1-based journal line where the chain breaks, 0 if intact
	HeadMismatch bool     // journal tail differs from the recorded head
}

// OK reports whether the folder matches its sealed snapshot and the journal is intact.
func (r *Report) OK() bool { return len(r.Drift) == 0 && r.ChainBroken == 0 && !r.HeadMismatch }

// Check compares the folder with the sealed snapshot and verifies the journal.
func (s *Store) Check() (*Report, error) {
	st, err := s.load()
	if err != nil {
		return nil, err
	}
	snap, err := Scan(s.Folder)
	if err != nil {
		return nil, err
	}
	rep := &Report{Drift: Diff(st.Sealed, snap)}
	rep.ChainBroken, rep.HeadMismatch, err = s.verifyChain(st)
	return rep, err
}

// Seal acknowledges the folder's current state. It is always an explicit act.
func (s *Store) Seal() (int, error) {
	st, err := s.load()
	if err != nil {
		return 0, err
	}
	snap, err := Scan(s.Folder)
	if err != nil {
		return 0, err
	}
	for _, c := range Diff(st.Seen, snap) {
		if err := s.append(st, Entry{Source: SourceDetected, Op: c.Op, File: c.File, Digest: snap[c.File]}); err != nil {
			return 0, err
		}
	}
	if err := s.append(st, Entry{Source: SourceUser, Op: "seal", Digest: snapshotDigest(snap)}); err != nil {
		return 0, err
	}
	st.Sealed, st.Seen = snap, snap
	return len(snap), s.save(st)
}

// Remove deletes the shadow store for this folder and nothing else. It refuses
// any path that is not a direct child of ~/.loomwork/folders holding a state file.
func (s *Store) Remove() (bool, error) {
	if !s.Tracked() || filepath.Dir(s.Dir) != s.root {
		return false, nil
	}
	return true, os.RemoveAll(s.Dir)
}

// Entries returns the journal, oldest first.
func (s *Store) Entries() ([]Entry, error) {
	f, err := os.Open(filepath.Join(s.Dir, journal))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Scan digests every regular file under dir. Hidden entries, symlinks, archives
// and their sidecars are skipped; a folder over maxFiles is refused.
func Scan(dir string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if strings.HasSuffix(rel, ".aci") || strings.HasSuffix(rel, ".slsa.json") || strings.HasSuffix(rel, ".attestation.json") {
			return nil
		}
		if len(out) >= maxFiles {
			return fmt.Errorf("more than %d files: %w", maxFiles, ErrNotApplicable)
		}
		h, err := digestFile(path)
		if err != nil {
			return nil
		}
		out[filepath.ToSlash(rel)] = h
		return nil
	})
	return out, err
}

// Diff lists what changed from old to cur, sorted by file name.
func Diff(old, cur map[string]string) []Change {
	var out []Change
	for f, h := range cur {
		if oh, ok := old[f]; !ok {
			out = append(out, Change{"added", f})
		} else if oh != h {
			out = append(out, Change{"changed", f})
		}
	}
	for f := range old {
		if _, ok := cur[f]; !ok {
			out = append(out, Change{"removed", f})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

func (s *Store) verifyChain(st *state) (brokenAt int, headMismatch bool, err error) {
	f, err := os.Open(filepath.Join(s.Dir, journal))
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	prev, n := "", 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		n++
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Previous != prev {
			return n, false, nil
		}
		prev = lineDigest(sc.Bytes())
	}
	if err := sc.Err(); err != nil {
		return 0, false, err
	}
	return 0, prev != st.Head, nil
}

func (s *Store) append(st *state, e Entry) error {
	e.Time, e.Previous = now(), st.Head
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, journal), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	st.Head = lineDigest(b)
	return nil
}

func (s *Store) load() (*state, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, stateFn))
	if err != nil {
		return nil, err
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("shadow state %s: %w", filepath.Join(s.Dir, stateFn), err)
	}
	return &st, nil
}

// save writes state.json atomically (temp file in the same directory, rename).
func (s *Store) save(st *state) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, filepath.Join(s.Dir, stateFn))
}

func digestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func snapshotDigest(snap map[string]string) string {
	names := make([]string, 0, len(snap))
	for n := range snap {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s %s\n", snap[n], n)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func lineDigest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func now() string { return time.Now().UTC().Format(time.RFC3339) }
