package memory

import (
	"database/sql"
	"encoding/json"
	"time"
)

// Stage holds a proposal outside the record store. A staged proposal has no
// derivative edges, embedding or search entry and is invisible to every view
// except a reviewer's, until PromoteProposal moves it into the record store.
func (s *Store) Stage(r *Record) error {
	if r.CreatedAt == "" {
		r.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		r.UpdatedAt = r.CreatedAt
	}
	if r.Retention.Mode == "ttl" && r.Retention.ExpiresAt == "" {
		r.Retention.ExpiresAt = time.Now().UTC().Add(time.Duration(r.Retention.TTLSeconds) * time.Second).Format(time.RFC3339)
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return err
	}
	enc, err := s.encrypt(payload)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR REPLACE INTO proposals (id, payload_enc, created_at) VALUES (?, ?, ?)`,
		r.ID, enc, r.CreatedAt)
	return err
}

// GetProposal returns a staged proposal, or nil if there is none.
func (s *Store) GetProposal(id string) (*Record, error) {
	var enc []byte
	err := s.db.QueryRow(`SELECT payload_enc FROM proposals WHERE id = ?`, id).Scan(&enc)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.decodeRecord(enc)
}

// ListProposals returns staged proposals, newest first. A non-empty writer
// limits the result to that writer's proposals.
func (s *Store) ListProposals(writer string) ([]*Record, error) {
	rows, err := s.db.Query(`SELECT payload_enc FROM proposals ORDER BY created_at DESC, rowid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Record
	for rows.Next() {
		var enc []byte
		if err := rows.Scan(&enc); err != nil {
			return nil, err
		}
		r, err := s.decodeRecord(enc)
		if err != nil {
			return nil, err
		}
		if writer == "" || r.Provenance.WriterAgentID == writer {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

// StagedIDs lists the IDs of staged proposals without decrypting them.
func (s *Store) StagedIDs() ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM proposals`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeleteProposal removes a staged proposal (secure_delete overwrites the page).
func (s *Store) DeleteProposal(id string) error {
	_, err := s.db.Exec(`DELETE FROM proposals WHERE id = ?`, id)
	return err
}

// PromoteProposal moves a staged proposal into the record store as active.
func (s *Store) PromoteProposal(id string) error {
	r, err := s.GetProposal(id)
	if err != nil || r == nil {
		return err
	}
	r.Status = StatusActive
	clearPendingTTL(r)
	if err := s.Write(r); err != nil {
		return err
	}
	return s.DeleteProposal(id)
}

// PendingCount counts records awaiting review: pending records plus staged
// proposals. A non-empty writer limits the count to that writer.
func (s *Store) PendingCount(writer string) (int, error) {
	pending, err := s.Query(QueryFilter{Status: StatusPending})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range pending {
		if !r.IsExpired() && (writer == "" || r.Provenance.WriterAgentID == writer) {
			n++
		}
	}
	staged, err := s.ListProposals(writer)
	if err != nil {
		return 0, err
	}
	for _, r := range staged {
		if !r.IsExpired() {
			n++
		}
	}
	return n, nil
}

func (s *Store) decodeRecord(enc []byte) (*Record, error) {
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

// clampPendingRetention bounds how long an agent's proposal may stay pending:
// the effective expiry is min(the writer's own TTL, the pending TTL), whatever
// the writer asked for. The writer's own choice is remembered and takes effect
// on approval. Any writer-supplied ExpiresAt is discarded and recomputed from
// now, so an agent cannot pre-set a far-future expiry.
func clampPendingRetention(req Retention, pendingTTL time.Duration) Retention {
	if pendingTTL <= 0 {
		return req
	}
	pending := int64(pendingTTL / time.Second)
	if req.Mode == "ttl" && req.TTLSeconds > 0 && req.TTLSeconds <= pending {
		return Retention{Mode: "ttl", TTLSeconds: req.TTLSeconds}
	}
	out := Retention{Mode: "ttl", TTLSeconds: pending, PendingTTL: true, RequestedMode: req.Mode}
	if req.Mode == "ttl" {
		out.RequestedTTLSeconds = req.TTLSeconds
	}
	return out
}

// clearPendingTTL is applied on approval: it swaps the runtime-imposed pending
// expiry for the retention the writer asked for (a TTL restarts from approval).
func clearPendingTTL(r *Record) {
	if !r.Retention.PendingTTL {
		return
	}
	if r.Retention.RequestedMode == "ttl" && r.Retention.RequestedTTLSeconds > 0 {
		r.Retention = Retention{Mode: "ttl", TTLSeconds: r.Retention.RequestedTTLSeconds}
		return
	}
	r.Retention = Retention{Mode: "until_revoked"}
}
