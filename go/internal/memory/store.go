package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"loomwork.dev/loomwork/internal/secret"
)

// Store is the user-controlled raw memory store.
//
// The store is the source of truth. Agents never read it directly: they read
// through a View (see view.go) which applies consent, retention, sensitivity
// and status filters. Each record's full content is encrypted at rest in one
// payload; the plaintext columns hold only what queries need (kind, status,
// sensitivity, timestamps).
type Store struct {
	db  *sql.DB
	box *secret.Box
}

// OpenDefaultStore opens ~/.loomwork/memory.db with the shared encryption key.
func OpenDefaultStore(home string) (*Store, error) {
	pass, err := secret.MemoryPassphrase(home)
	if err != nil {
		return nil, err
	}
	return OpenStore(secret.DefaultDBPath(home), pass)
}

// OpenStore opens (or creates) a typed memory store at the given path. An
// empty passphrase stores plaintext and is meant for tests only.
func OpenStore(dbPath string, passphrase string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	// One connection: the pragmas below then apply to every statement, and
	// writes are serialised. busy_timeout lets a second process (the
	// conversation store shares this file) wait instead of failing.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		db.Close()
		return nil, err
	}
	// Overwrite deleted content so revoked data does not linger in free pages.
	if _, err := db.Exec(`PRAGMA secure_delete = ON`); err != nil {
		db.Close()
		return nil, err
	}
	box, err := secret.NewBox(dbPath, passphrase)
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, box: box}
	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, err
	}
	// Records past their TTL are deleted, not merely hidden.
	if _, err := s.PurgeExpired(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// PurgeExpired revokes (deletes the content of) every record whose TTL has
// passed and returns how many were purged.
func (s *Store) PurgeExpired() (int, error) {
	// Tombstones (revoked/rejected) have nothing left to expire; skip them in SQL.
	all, err := s.Query(QueryFilter{ExcludeTerminal: true})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range all {
		if r.Status != StatusRevoked && r.Status != StatusRejected && r.IsExpired() {
			if _, err := s.Revoke(r.ID); err != nil {
				return n, err
			}
			n++
		}
	}
	// Staged (write-gated) proposals expire the same way.
	staged, err := s.ListProposals("")
	if err != nil {
		return n, err
	}
	for _, r := range staged {
		if r.IsExpired() {
			if err := s.DeleteProposal(r.ID); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

func (s *Store) initSchema() error {
	_, err := s.db.Exec(`
                CREATE TABLE IF NOT EXISTS records (
                        id          TEXT PRIMARY KEY,
                        kind        TEXT NOT NULL,
                        status      TEXT NOT NULL,
                        sensitivity TEXT NOT NULL,
                        payload_enc BLOB NOT NULL,
                        provenance  TEXT NOT NULL,
                        consent     TEXT NOT NULL,
                        retention   TEXT NOT NULL,
                        derivatives TEXT NOT NULL DEFAULT '[]',
                        created_at  TEXT NOT NULL,
                        updated_at  TEXT NOT NULL,
                        revoked_at  TEXT
                );
                CREATE INDEX IF NOT EXISTS idx_kind   ON records(kind);
                CREATE INDEX IF NOT EXISTS idx_status ON records(status);
                CREATE INDEX IF NOT EXISTS idx_time   ON records(created_at);

                -- Separate embedding table so revocation can nuke embeddings independently.
                CREATE TABLE IF NOT EXISTS embeddings (
                        record_id TEXT PRIMARY KEY,
                        embedding BLOB NOT NULL,
                        model     TEXT NOT NULL,
                        dimensions INTEGER NOT NULL,
                        created_at TEXT NOT NULL,
                        FOREIGN KEY (record_id) REFERENCES records(id) ON DELETE CASCADE
                );

                -- Derivatives graph (parent_id -> child_id) for revocation cascade.
                CREATE TABLE IF NOT EXISTS derivatives (
                        parent_id TEXT NOT NULL,
                        child_id  TEXT NOT NULL,
                        PRIMARY KEY (parent_id, child_id)
                );
                CREATE INDEX IF NOT EXISTS idx_deriv_parent ON derivatives(parent_id);
                CREATE INDEX IF NOT EXISTS idx_deriv_child  ON derivatives(child_id);

                -- Write-gate staging: proposals held outside the record store
                -- (no derivatives, embeddings or search) until a reviewer approves.
                CREATE TABLE IF NOT EXISTS proposals (
                        id          TEXT PRIMARY KEY,
                        payload_enc BLOB NOT NULL,
                        created_at  TEXT NOT NULL
                );
        `)
	return err
}

func (s *Store) encrypt(plaintext []byte) ([]byte, error) { return s.box.Seal(plaintext) }
func (s *Store) decrypt(blob []byte) ([]byte, error)      { return s.box.Open(blob) }

// Write persists a record. If the record has a parent, the derivatives
// graph is updated.
func (s *Store) Write(r *Record) error {
	if r.CreatedAt == "" {
		r.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		r.UpdatedAt = r.CreatedAt
	} else {
		r.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	// Compute retention expiry if TTL
	if r.Retention.Mode == "ttl" && r.Retention.ExpiresAt == "" {
		exp := time.Now().UTC().Add(time.Duration(r.Retention.TTLSeconds) * time.Second)
		r.Retention.ExpiresAt = exp.Format(time.RFC3339)
	}

	payload, err := json.Marshal(r)
	if err != nil {
		return err
	}
	enc, err := s.encrypt(payload)
	if err != nil {
		return err
	}
	derivJSON, _ := json.Marshal(r.Derivatives)

	// The record, its derivative edges and its parents' child lists change
	// together or not at all.
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(
		`INSERT OR REPLACE INTO records
                 (id, kind, status, sensitivity, payload_enc, provenance, consent, retention, derivatives, created_at, updated_at, revoked_at)
                 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Kind, r.Status, r.Sensitivity, enc,
		"{}", "{}", "{}", // provenance, consent and retention live only in the encrypted payload
		string(derivJSON), r.CreatedAt, r.UpdatedAt, r.RevokedAt,
	)
	if err != nil {
		return err
	}

	// Register a derivative edge from every record this one depends on: its
	// parent, the evidence behind a belief, and the inputs of an episode.
	// Revoking any of them then revokes this record too.
	for _, dep := range r.dependencies() {
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO derivatives (parent_id, child_id) VALUES (?, ?)`,
			dep, r.ID,
		); err != nil {
			return err
		}
		if err := addToParentDerivatives(tx, s, dep, r.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// dbq is the part of *sql.DB and *sql.Tx the store needs, so helpers can run
// inside a transaction.
type dbq interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Dependencies lists the record IDs this record was derived from (its parent,
// a belief's evidence, an episode's inputs).
func (r *Record) Dependencies() []string { return r.dependencies() }

// dependencies lists the record IDs this record was derived from.
func (r *Record) dependencies() []string {
	var deps []string
	if r.Provenance.ParentID != "" {
		deps = append(deps, r.Provenance.ParentID)
	}
	if r.Belief != nil {
		deps = append(deps, r.Belief.Evidence...)
	}
	if r.Episode != nil {
		deps = append(deps, r.Episode.Inputs...)
	}
	seen := map[string]bool{r.ID: true}
	var out []string
	for _, d := range deps {
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

func addToParentDerivatives(q dbq, s *Store, parentID, childID string) error {
	parent, err := s.getWith(q, parentID)
	if err != nil || parent == nil || parent.Status == StatusRevoked || parent.Status == StatusRejected {
		return nil // parent may be revoked or missing; the edge table still records it
	}
	for _, d := range parent.Derivatives {
		if d == childID {
			return nil // already there
		}
	}
	parent.Derivatives = append(parent.Derivatives, childID)
	return updateDerivatives(q, parent)
}

func updateDerivatives(q dbq, r *Record) error {
	derivJSON, _ := json.Marshal(r.Derivatives)
	_, err := q.Exec(`UPDATE records SET derivatives = ?, updated_at = ? WHERE id = ?`,
		string(derivJSON), time.Now().UTC().Format(time.RFC3339), r.ID)
	return err
}

// Get retrieves a single record by ID. Returns nil if not found.
func (s *Store) Get(id string) (*Record, error) { return s.getWith(s.db, id) }

func (s *Store) getWith(q dbq, id string) (*Record, error) {
	row := q.QueryRow(
		`SELECT payload_enc FROM records WHERE id = ?`, id,
	)
	var enc []byte
	if err := row.Scan(&enc); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	payload, err := s.decrypt(enc)
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(payload, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Query returns records matching the filter, sorted by created_at descending.
func (s *Store) Query(filter QueryFilter) ([]*Record, error) {
	var args []interface{}
	where := []string{"1=1"}
	if filter.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, filter.Kind)
	}
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, filter.Status)
	}
	if filter.ExcludeTerminal {
		where = append(where, "status NOT IN ('revoked', 'rejected')")
	}
	// Note: We do NOT filter by agent at the SQL level — the View does the
	// fine-grained consent check after decrypting. SQLite's LIKE on encrypted
	// blobs would be unreliable. We over-fetch and filter in Go.
	q := `SELECT payload_enc FROM records WHERE ` + strings.Join(where, " AND ") + ` ORDER BY created_at DESC, rowid DESC`
	if filter.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d OFFSET %d", filter.Limit, filter.Offset)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return s.scanRows(rows)
}

// QueryFilter narrows a query.
//
// Agent consent is not a SQL filter: payloads are encrypted, so a View decrypts
// and filters in Go (see View.scan, which pages with Limit/Offset).
type QueryFilter struct {
	Kind            Kind
	Status          Status
	ExcludeTerminal bool // skip revoked and rejected tombstones
	Limit           int  // 0 = no limit
	Offset          int  // used with Limit
}

func (s *Store) scanRows(rows *sql.Rows) ([]*Record, error) {
	var out []*Record
	for rows.Next() {
		var enc []byte
		if err := rows.Scan(&enc); err != nil {
			return nil, err
		}
		payload, err := s.decrypt(enc)
		if err != nil {
			return nil, err
		}
		var r Record
		if err := json.Unmarshal(payload, &r); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// WriteEmbedding stores an embedding for a record.
func (s *Store) WriteEmbedding(recordID string, embedding []float32, model string) error {
	// Encode as JSON for simplicity (production: use a binary format)
	embJSON, err := json.Marshal(embedding)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO embeddings (record_id, embedding, model, dimensions, created_at)
                 VALUES (?, ?, ?, ?, ?)`,
		recordID, embJSON, model, len(embedding), time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

// GetEmbedding retrieves an embedding.
func (s *Store) GetEmbedding(recordID string) ([]float32, string, error) {
	row := s.db.QueryRow(`SELECT embedding, model FROM embeddings WHERE record_id = ?`, recordID)
	var embJSON []byte
	var model string
	if err := row.Scan(&embJSON, &model); err != nil {
		if err == sql.ErrNoRows {
			return nil, "", nil
		}
		return nil, "", err
	}
	var emb []float32
	if err := json.Unmarshal(embJSON, &emb); err != nil {
		return nil, "", err
	}
	return emb, model, nil
}

// DeleteEmbedding removes an embedding (called by revocation cascade).
func (s *Store) DeleteEmbedding(recordID string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM embeddings WHERE record_id = ?`, recordID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetChildren returns direct child records of a parent.
func (s *Store) GetChildren(parentID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT child_id FROM derivatives WHERE parent_id = ?`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Close closes the underlying DB.
func (s *Store) Close() error {
	return s.db.Close()
}

// Stats returns counts for the doctor command.
type Stats struct {
	Total      int            `json:"total"`
	ByKind     map[Kind]int   `json:"byKind"`
	ByStatus   map[Status]int `json:"byStatus"`
	Embeddings int            `json:"embeddings"`
	Staged     int            `json:"staged"` // write-gated proposals in the proposals table
}

// ComputeStats returns summary stats.
func (s *Store) ComputeStats() (*Stats, error) {
	st := &Stats{ByKind: map[Kind]int{}, ByStatus: map[Status]int{}}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM proposals`).Scan(&st.Staged); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT kind, status FROM records`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, st2 string
		if err := rows.Scan(&k, &st2); err != nil {
			return nil, err
		}
		st.Total++
		st.ByKind[Kind(k)]++
		st.ByStatus[Status(st2)]++
	}
	row := s.db.QueryRow(`SELECT COUNT(*) FROM embeddings`)
	if err := row.Scan(&st.Embeddings); err != nil {
		return nil, err
	}
	return st, nil
}

// SortedKeys returns the keys of a map sorted (helper for tests/debug).
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
