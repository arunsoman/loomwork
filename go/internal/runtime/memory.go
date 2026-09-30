// Package runtime implements the local mesh runtime: loads ACIs, runs them
// with Ollama as the LLM backend, persists memory to a local SQLite DB.
package runtime

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"loomwork.dev/loomwork/internal/secret"
)

// Memory is the local SQLite-backed conversation memory: timestamped entries,
// an episodic timeline and subject-predicate-object triples. Content is
// encrypted at rest with AES-256-GCM. Vector similarity is not implemented.
type Memory struct {
	db  *sql.DB
	box *secret.Box
}

// MemoryEntry is a single memory write.
type MemoryEntry struct {
	ID        string `json:"entry_id"`
	AgentID   string `json:"agent_id"`
	ACI       string `json:"aci"`
	Kind      string `json:"kind"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

// OpenDefaultMemory opens ~/.loomwork/memory.db encrypted at rest. The
// passphrase is LOOMWORK_MEMORY_PASSPHRASE if set, otherwise a random key
// generated once and stored (0600) in ~/.loomwork/memory.key.
func OpenDefaultMemory(home string) (*Memory, error) {
	pass, err := secret.MemoryPassphrase(home)
	if err != nil {
		return nil, err
	}
	return OpenMemory(secret.DefaultDBPath(home), pass)
}

// OpenMemory opens (or creates) a memory DB at the given path. An empty
// passphrase stores plaintext and is meant for tests only.
func OpenMemory(dbPath string, passphrase string) (*Memory, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	// One connection (so the pragmas apply to every statement) and a busy
	// timeout, because the typed store opens this same file.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		db.Close()
		return nil, err
	}
	// Overwrite deleted content so it does not linger in free pages.
	if _, err := db.Exec(`PRAGMA secure_delete = ON`); err != nil {
		db.Close()
		return nil, err
	}
	box, err := secret.NewBox(dbPath, passphrase)
	if err != nil {
		db.Close()
		return nil, err
	}
	m := &Memory{db: db, box: box}
	if err := m.initSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return m, nil
}

func (m *Memory) initSchema() error {
	_, err := m.db.Exec(`
		CREATE TABLE IF NOT EXISTS entries (
			entry_id    TEXT PRIMARY KEY,
			agent_id    TEXT NOT NULL,
			aci         TEXT NOT NULL,
			kind        TEXT NOT NULL,
			content_enc BLOB NOT NULL,
			metadata    TEXT,
			timestamp   TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_agent ON entries(agent_id);
		CREATE INDEX IF NOT EXISTS idx_kind  ON entries(kind);
		CREATE INDEX IF NOT EXISTS idx_time  ON entries(timestamp);
		CREATE TABLE IF NOT EXISTS graph (
			subject TEXT NOT NULL,
			predicate TEXT NOT NULL,
			object TEXT NOT NULL,
			entry_id TEXT,
			PRIMARY KEY (subject, predicate, object)
		);
		CREATE INDEX IF NOT EXISTS idx_graph_subj ON graph(subject);
		CREATE TABLE IF NOT EXISTS episodic (
			ts TEXT NOT NULL,
			event TEXT NOT NULL,
			entry_id TEXT,
			agent_id TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_ep_ts ON episodic(ts);
	`)
	return err
}

func (m *Memory) encrypt(plaintext string) ([]byte, error) {
	return m.box.Seal([]byte(plaintext))
}

func (m *Memory) decrypt(blob []byte) (string, error) {
	pt, err := m.box.Open(blob)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// Write persists an entry. Returns the entry with timestamp filled in.
func (m *Memory) Write(entry *MemoryEntry) error {
	if entry.ID == "" {
		entry.ID = newID("mem")
	}
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	enc, err := m.encrypt(entry.Content)
	if err != nil {
		return err
	}
	// The entry and its timeline row are written together. A rewritten entry
	// (same ID, e.g. a re-indexed folder) replaces its timeline row instead of
	// adding a stale duplicate.
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(
		`INSERT OR REPLACE INTO entries (entry_id, agent_id, aci, kind, content_enc, metadata, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		entry.ID, entry.AgentID, entry.ACI, entry.Kind, enc, "{}", entry.Timestamp,
	); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM episodic WHERE entry_id = ?`, entry.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(
		`INSERT INTO episodic (ts, event, entry_id, agent_id) VALUES (?, ?, ?, ?)`,
		entry.Timestamp, entry.Kind+":"+entry.ID, entry.ID, entry.AgentID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// QueryByAgent returns the most recent entries for an agent.
func (m *Memory) QueryByAgent(agentID string, limit int) ([]*MemoryEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := m.db.Query(
		`SELECT entry_id, agent_id, aci, kind, content_enc, timestamp
		 FROM entries WHERE agent_id = ? ORDER BY timestamp DESC, rowid DESC LIMIT ?`,
		agentID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return m.scanRows(rows)
}

// QueryConversation returns the agent's most recent user/assistant turns,
// newest first, ignoring other entry kinds (folder indexes, usage records).
func (m *Memory) QueryConversation(agentID string, limit int) ([]*MemoryEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := m.db.Query(
		`SELECT entry_id, agent_id, aci, kind, content_enc, timestamp
		 FROM entries WHERE agent_id = ? AND kind IN ('user_message','assistant_response')
		 ORDER BY timestamp DESC, rowid DESC LIMIT ?`,
		agentID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return m.scanRows(rows)
}

// SumUsage adds up the token counts recorded with kind "token_usage" for an agent.
func (m *Memory) SumUsage(agentID string) (int, error) {
	rows, err := m.db.Query(`SELECT content_enc FROM entries WHERE agent_id = ? AND kind = 'token_usage'`, agentID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	total := 0
	for rows.Next() {
		var enc []byte
		if err := rows.Scan(&enc); err != nil {
			return 0, err
		}
		c, err := m.decrypt(enc)
		if err != nil {
			return 0, err
		}
		var n int
		fmt.Sscanf(c, "%d", &n)
		total += n
	}
	return total, rows.Err()
}

// QueryByKind returns the most recent entries of a specific kind.
func (m *Memory) QueryByKind(kind string, limit int) ([]*MemoryEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := m.db.Query(
		`SELECT entry_id, agent_id, aci, kind, content_enc, timestamp
		 FROM entries WHERE kind = ? ORDER BY timestamp DESC, rowid DESC LIMIT ?`,
		kind, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return m.scanRows(rows)
}

func (m *Memory) scanRows(rows *sql.Rows) ([]*MemoryEntry, error) {
	var out []*MemoryEntry
	for rows.Next() {
		var e MemoryEntry
		var enc []byte
		if err := rows.Scan(&e.ID, &e.AgentID, &e.ACI, &e.Kind, &enc, &e.Timestamp); err != nil {
			return nil, err
		}
		content, err := m.decrypt(enc)
		if err != nil {
			return nil, err
		}
		e.Content = content
		out = append(out, &e)
	}
	return out, rows.Err()
}

// AddGraphTriple adds a semantic triple.
func (m *Memory) AddGraphTriple(subject, predicate, object string) error {
	_, err := m.db.Exec(
		`INSERT OR REPLACE INTO graph (subject, predicate, object) VALUES (?, ?, ?)`,
		subject, predicate, object,
	)
	return err
}

// QueryGraph returns triples matching the subject (and optionally predicate).
func (m *Memory) QueryGraph(subject string, predicate string) ([][3]string, error) {
	var rows *sql.Rows
	var err error
	if predicate != "" {
		rows, err = m.db.Query(
			`SELECT subject, predicate, object FROM graph WHERE subject = ? AND predicate = ?`,
			subject, predicate,
		)
	} else {
		rows, err = m.db.Query(
			`SELECT subject, predicate, object FROM graph WHERE subject = ?`,
			subject,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][3]string
	for rows.Next() {
		var s, p, o string
		if err := rows.Scan(&s, &p, &o); err != nil {
			return nil, err
		}
		out = append(out, [3]string{s, p, o})
	}
	return out, rows.Err()
}

// Timeline returns the most recent episodic events.
func (m *Memory) Timeline(limit int) ([]map[string]string, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := m.db.Query(
		`SELECT ts, event, entry_id, agent_id FROM episodic ORDER BY ts DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		var ts, event, entryID, agentID string
		if err := rows.Scan(&ts, &event, &entryID, &agentID); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{
			"ts": ts, "event": event, "entry_id": entryID, "agent_id": agentID,
		})
	}
	return out, rows.Err()
}

// Close closes the underlying DB.
func (m *Memory) Close() error {
	return m.db.Close()
}

// newID returns a prefixed, time-ordered ID with a random suffix.
func newID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s_%d_%s", prefix, time.Now().UnixNano(), hex.EncodeToString(b))
}
