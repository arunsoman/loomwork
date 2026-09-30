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
	limits     Limits
	writeGate  bool
}

// Options tunes an agent view.
type Options struct {
	Limits    Limits
	WriteGate bool // proposals are staged outside the record store until approved
}

// NewView constructs a filtered view for an agent with the default limits
// (see DefaultLimits) and ordinary pending-record writes.
func NewView(store *Store, agentID string, scopes []string) *View {
	return NewViewOpts(store, agentID, scopes, Options{Limits: DefaultLimits(), WriteGate: WriteGateFromEnv()})
}

// NewViewOpts constructs a filtered view with explicit options.
func NewViewOpts(store *Store, agentID string, scopes []string, o Options) *View {
	return &View{store: store, agentID: agentID, scopes: scopes, limits: o.Limits, writeGate: o.WriteGate}
}

// NewReviewerView constructs a view that can approve pending records.
// Typically used by the user's CLI or a policy engine.
func NewReviewerView(store *Store) *View {
	return &View{store: store, agentID: "user", scopes: []string{"reviewer"}, canApprove: true}
}

// scanPage is how many rows scan fetches (and decrypts) at a time.
const scanPage = 200

// scan walks the store a page at a time and calls keep for every record this
// view may see, stopping once limit records have been kept. Consent is checked
// after decryption, so paging (rather than one unbounded query) is what keeps a
// large store from being loaded whole to return the first few records. A
// reviewer view also walks staged (write-gated) proposals.
func (v *View) scan(kind Kind, limit int, keep func(*Record) bool) ([]*Record, error) {
	var out []*Record
	for offset := 0; ; offset += scanPage {
		page, err := v.store.Query(QueryFilter{Kind: kind, Limit: scanPage, Offset: offset})
		if err != nil {
			return nil, err
		}
		for _, r := range page {
			if v.sees(r) && keep(r) {
				out = append(out, r)
				if len(out) >= limit {
					return out, nil
				}
			}
		}
		if len(page) < scanPage {
			break
		}
	}
	if v.canApprove {
		staged, err := v.store.ListProposals("")
		if err != nil {
			return nil, err
		}
		for _, r := range staged {
			if (kind == "" || r.Kind == kind) && v.sees(r) && keep(r) {
				out = append(out, r)
				if len(out) >= limit {
					return out, nil
				}
			}
		}
	}
	return out, nil
}

// List returns records of a kind (all kinds if empty) visible to this view,
// newest first, up to limit (default 20).
func (v *View) List(kind Kind, limit int) ([]*Record, error) {
	if limit <= 0 {
		limit = 20
	}
	return v.scan(kind, limit, func(*Record) bool { return true })
}

// ListStatus is List restricted to one status (all statuses if empty), applied
// before the limit so a filtered listing is not cut short by other records.
func (v *View) ListStatus(kind Kind, status Status, limit int) ([]*Record, error) {
	if limit <= 0 {
		limit = 20
	}
	return v.scan(kind, limit, func(r *Record) bool { return status == "" || r.Status == status })
}

// Get returns a single record if visible to this view.
func (v *View) Get(id string) (*Record, error) {
	r, err := v.store.Get(id)
	if err != nil {
		return nil, err
	}
	if r == nil && v.canApprove {
		r, err = v.store.GetProposal(id)
		if err != nil {
			return nil, err
		}
	}
	if r == nil {
		return nil, nil
	}
	if !v.sees(r) {
		return nil, nil // not visible = doesn't exist, from the agent's POV
	}
	return r, nil
}

// Propose creates a pending record. The record is NOT visible to other agents
// until a reviewer approves it (status: pending → active).
//
// Agents propose; the user (or a policy) decides what becomes durable.
//
// Agent writers (not the reviewer) are held to the view's limits: a proposal
// that would exceed the per-writer pending cap is refused, and an unapproved
// proposal expires after the pending TTL. With the write gate on, the proposal
// is staged outside the record store and the writer cannot read it back.
func (v *View) Propose(r *Record) error {
	r.Status = StatusPending
	r.Provenance.WriterAgentID = v.agentID
	if r.Provenance.WrittenAt == "" {
		r.Provenance.WrittenAt = time.Now().UTC().Format(time.RFC3339)
	}
	if !v.canApprove {
		if max := v.limits.MaxPendingPerWriter; max > 0 {
			n, err := v.store.PendingCount(v.agentID)
			if err != nil {
				return err
			}
			if n >= max {
				return fmt.Errorf("pending limit reached: %s already has %d proposals awaiting review (cap %d); approve or reject some with `loomwork memory`", v.agentID, n, max)
			}
		}
		r.Retention = clampPendingRetention(r.Retention, v.limits.PendingTTL)
	}
	if v.writeGate {
		return v.store.Stage(r)
	}
	return v.store.Write(r)
}

// sees reports whether this view may read r. A reviewer view (the user's CLI,
// created only by NewReviewerView) also reads other writers' unexpired pending
// records; that privilege is a property of the view, not a scope an agent
// could be handed.
func (v *View) sees(r *Record) bool {
	if v.canApprove && r.Status == StatusPending && !r.IsExpired() {
		return true
	}
	return r.IsVisibleTo(v.agentID, v.scopes)
}

// PendingCount is the number of this agent's proposals still awaiting review.
func (v *View) PendingCount() (int, error) {
	return v.store.PendingCount(v.agentID)
}

// Approve promotes a pending record to active. Only reviewer views can do this.
func (v *View) Approve(recordID string) error {
	if !v.canApprove {
		return fmt.Errorf("this view cannot approve records (use a reviewer view)")
	}
	if staged, err := v.store.GetProposal(recordID); err != nil {
		return err
	} else if staged != nil {
		return v.store.PromoteProposal(recordID)
	}
	r, err := v.store.Get(recordID)
	if err != nil || r == nil {
		return fmt.Errorf("record not found: %s", recordID)
	}
	if r.Status != StatusPending {
		return fmt.Errorf("record is not pending (status=%s)", r.Status)
	}
	r.Status = StatusActive
	clearPendingTTL(r)
	return v.store.Write(r)
}

// Reject declines a pending record and deletes its content.
func (v *View) Reject(recordID string) error {
	if !v.canApprove {
		return fmt.Errorf("this view cannot reject records")
	}
	if staged, err := v.store.GetProposal(recordID); err != nil {
		return err
	} else if staged != nil {
		return v.store.DeleteProposal(recordID)
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
	q := strings.ToLower(query)
	return v.scan("", limit, func(r *Record) bool {
		return strings.Contains(strings.ToLower(r.contentString()), q)
	})
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
