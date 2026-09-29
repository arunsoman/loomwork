package memory

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// View is a per-agent filtered view of the raw store.
//
// The View applies:
//   - Consent filter (only records the agent is allowed to see)
//   - Retention filter (TTL-expired records are invisible)
//   - Sensitivity filter (high-sensitivity records require the "sensitive" scope)
//   - Status filter (pending records visible only to the writer; revoked and
//     rejected records invisible)
//
// Writes go through Propose(), which creates a pending record that a reviewer
// (the user, or a policy) must approve before it becomes active.
type View struct {
	store      *Store
	agentID    string
	scopes     []string
	canApprove bool // if true, this view can approve pending records
}

// NewView constructs a filtered view for an agent.
func NewView(store *Store, agentID string, scopes []string) *View {
	return &View{store: store, agentID: agentID, scopes: scopes}
}

// NewReviewerView constructs a view that can approve pending records.
// Typically used by the user's CLI or a policy engine.
func NewReviewerView(store *Store) *View {
	return &View{store: store, agentID: "user", scopes: []string{"reviewer"}, canApprove: true}
}

// List returns records of a kind (all kinds if empty) visible to this view,
// newest first, up to limit (default 20).
func (v *View) List(kind Kind, limit int) ([]*Record, error) {
	if limit <= 0 {
		limit = 20
	}
	records, err := v.store.Query(QueryFilter{Kind: kind})
	if err != nil {
		return nil, err
	}
	var out []*Record
	for _, r := range records {
		if !r.IsVisibleTo(v.agentID, v.scopes) {
			continue
		}
		out = append(out, r)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Get returns a single record if visible to this view.
func (v *View) Get(id string) (*Record, error) {
	r, err := v.store.Get(id)
	if err != nil || r == nil {
		return nil, err
	}
	if !r.IsVisibleTo(v.agentID, v.scopes) {
		return nil, nil // not visible = doesn't exist, from the agent's POV
	}
	return r, nil
}

// Propose creates a pending record. The record is NOT visible to other agents
// until a reviewer approves it (status: pending → active).
//
// Agents propose; the user (or a policy) decides what becomes durable.
func (v *View) Propose(r *Record) error {
	r.Status = StatusPending
	r.Provenance.WriterAgentID = v.agentID
	if r.Provenance.WrittenAt == "" {
		r.Provenance.WrittenAt = time.Now().UTC().Format(time.RFC3339)
	}
	return v.store.Write(r)
}

// Approve promotes a pending record to active. Only reviewer views can do this.
func (v *View) Approve(recordID string) error {
	if !v.canApprove {
		return fmt.Errorf("this view cannot approve records (use a reviewer view)")
	}
	r, err := v.store.Get(recordID)
	if err != nil || r == nil {
		return fmt.Errorf("record not found: %s", recordID)
	}
	if r.Status != StatusPending {
		return fmt.Errorf("record is not pending (status=%s)", r.Status)
	}
	r.Status = StatusActive
	return v.store.Write(r)
}

// Reject declines a pending record and deletes its content.
func (v *View) Reject(recordID string) error {
	if !v.canApprove {
		return fmt.Errorf("this view cannot reject records")
	}
	return v.store.Reject(recordID)
}

// Revoke triggers the revocation cascade. Reviewer views only.
func (v *View) Revoke(recordID string) (*RevocationResult, error) {
	if !v.canApprove {
		return nil, fmt.Errorf("this view cannot revoke records")
	}
	return v.store.Revoke(recordID)
}

// Search performs a case-insensitive keyword search over the content of
// visible records (not their IDs or field names).
func (v *View) Search(query string, limit int) ([]*Record, error) {
	if limit <= 0 {
		limit = 10
	}
	all, err := v.store.Query(QueryFilter{})
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(query)
	var out []*Record
	for _, r := range all {
		if !r.IsVisibleTo(v.agentID, v.scopes) {
			continue
		}
		if strings.Contains(strings.ToLower(r.contentString()), q) {
			out = append(out, r)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

// contentString returns the searchable text of a record's typed payload.
func (r *Record) contentString() string {
	var payload interface{}
	switch {
	case r.Preference != nil:
		payload = r.Preference
	case r.Episode != nil:
		payload = r.Episode
	case r.Artifact != nil:
		payload = r.Artifact
	case r.Belief != nil:
		payload = r.Belief
	case r.Failure != nil:
		payload = r.Failure
	default:
		return ""
	}
	var parts []string
	b, _ := json.Marshal(payload)
	var m map[string]interface{}
	if json.Unmarshal(b, &m) == nil {
		for _, val := range m {
			switch t := val.(type) {
			case string:
				parts = append(parts, t)
			case []interface{}:
				for _, x := range t {
					if sx, ok := x.(string); ok {
						parts = append(parts, sx)
					}
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}
