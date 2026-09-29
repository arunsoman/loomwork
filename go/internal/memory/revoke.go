package memory

import (
	"fmt"
	"time"
)

// RevocationResult captures what a revocation deleted.
type RevocationResult struct {
	RevokedRecords    []string `json:"revokedRecords"`
	DeletedEmbeddings int      `json:"deletedEmbeddings"`
	Depth             int      `json:"depth"` // how deep the cascade went
}

// Revoke deletes a record's content and embedding and cascades to every record
// derived from it: children by parent link, beliefs citing it as evidence, and
// episodes that used it as an input.
//
// Revocation is a real delete, not a hidden flag. The record is replaced by a
// tombstone (ID, kind, status, timestamps and derivative links only), and the
// database overwrites freed pages (secure_delete). It cannot be undone; to
// bring a fact back, propose it again.
func (s *Store) Revoke(recordID string) (*RevocationResult, error) {
	result := &RevocationResult{}
	visited := map[string]bool{}
	if err := s.revokeCascade(recordID, StatusRevoked, result, visited, 0); err != nil {
		return nil, err
	}
	return result, nil
}

// Reject declines a pending proposal: its content is deleted and it never
// becomes durable.
func (s *Store) Reject(recordID string) error {
	r, err := s.Get(recordID)
	if err != nil {
		return err
	}
	if r == nil {
		return fmt.Errorf("record not found: %s", recordID)
	}
	if r.Status != StatusPending {
		return fmt.Errorf("record is not pending (status=%s)", r.Status)
	}
	return s.tombstone(r, StatusRejected)
}

// tombstone replaces a record with a content-free marker and drops its embedding.
func (s *Store) tombstone(r *Record, status Status) error {
	now := time.Now().UTC().Format(time.RFC3339)
	t := &Record{
		ID:          r.ID,
		Kind:        r.Kind,
		Status:      status,
		Sensitivity: r.Sensitivity,
		Derivatives: r.Derivatives,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   now,
		RevokedAt:   now,
	}
	if err := s.Write(t); err != nil {
		return fmt.Errorf("delete content of %s: %w", r.ID, err)
	}
	return nil
}

func (s *Store) revokeCascade(recordID string, status Status, result *RevocationResult, visited map[string]bool, depth int) error {
	if visited[recordID] {
		return nil // cycle guard
	}
	visited[recordID] = true
	if depth > result.Depth {
		result.Depth = depth
	}

	r, err := s.Get(recordID)
	if err != nil {
		return err
	}
	if r != nil && r.Status != StatusRevoked && r.Status != StatusRejected {
		if err := s.tombstone(r, status); err != nil {
			return err
		}
		result.RevokedRecords = append(result.RevokedRecords, recordID)
	}
	// Even for a missing or already revoked record, make sure no embedding is left.
	deleted, err := s.DeleteEmbedding(recordID)
	if err != nil {
		return fmt.Errorf("delete embedding %s: %w", recordID, err)
	}
	if deleted {
		result.DeletedEmbeddings++
	}

	children, err := s.GetChildren(recordID)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := s.revokeCascade(child, StatusRevoked, result, visited, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// UnRevoke is not supported: revocation deletes content, so there is nothing
// to restore. It always returns an error explaining this.
func (s *Store) UnRevoke(recordID string) error {
	return fmt.Errorf("cannot restore %s: revocation deletes the record's content; propose it again instead", recordID)
}

// Supersede marks oldRecordID as replaced by newRecordID. The old record stays
// readable for audit; the new record is independent, so revoking the old one
// does not revoke its replacement.
func (s *Store) Supersede(oldRecordID, newRecordID string) error {
	old, err := s.Get(oldRecordID)
	if err != nil {
		return err
	}
	if old == nil {
		return fmt.Errorf("old record not found: %s", oldRecordID)
	}
	if nw, err := s.Get(newRecordID); err != nil {
		return err
	} else if nw == nil {
		return fmt.Errorf("new record not found: %s", newRecordID)
	}
	old.Status = StatusSuperseded
	if old.Belief != nil {
		old.Belief.SupersededBy = newRecordID
	}
	return s.Write(old)
}
