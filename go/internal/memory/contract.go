// Package memory implements the typed memory contract.
//
// Five typed record kinds, all sharing a common envelope:
//   - Preference: user-stated or agent-inferred preference
//   - Episode:    timestamped interaction event
//   - Artifact:   durable output (doc, code, decision)
//   - Belief:     inferred claim about the world (cited)
//   - Failure:    known-bad-path warning (first-class)
//
// Every record carries:
//   - provenance (who wrote it, when, from what source, parent ID if derived)
//   - consent scope (which agents/scopes may see it)
//   - retention (TTL or "until_revoked")
//   - sensitivity (low|medium|high — affects encryption + logging)
//   - derivatives (list of record IDs derived from this one — for revocation)
//   - status (pending | active | superseded | revoked)
//
// Revoking a record deletes its content and embedding and cascades, through
// the derivatives graph, to everything derived from it (see revoke.go).
package memory

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"time"
)

// Pending hygiene defaults. Unapproved proposals from agents expire on their
// own, and one writer cannot pile up an unbounded review queue.
const (
	DefaultPendingTTL          = 7 * 24 * time.Hour
	DefaultMaxPendingPerWriter = 50
)

// Limits bounds what agent writers may leave pending.
type Limits struct {
	PendingTTL          time.Duration // 0 = pending records never expire
	MaxPendingPerWriter int           // 0 = unlimited
}

// DefaultLimits returns the built-in limits, overridden by
// LOOMWORK_PENDING_TTL (seconds) and LOOMWORK_PENDING_CAP when set.
func DefaultLimits() Limits {
	l := Limits{PendingTTL: DefaultPendingTTL, MaxPendingPerWriter: DefaultMaxPendingPerWriter}
	if v, err := strconv.ParseInt(os.Getenv("LOOMWORK_PENDING_TTL"), 10, 64); err == nil && v >= 0 {
		l.PendingTTL = time.Duration(v) * time.Second
	}
	if v, err := strconv.Atoi(os.Getenv("LOOMWORK_PENDING_CAP")); err == nil && v >= 0 {
		l.MaxPendingPerWriter = v
	}
	return l
}

// WriteGateFromEnv reports whether LOOMWORK_WRITE_GATE requests gated writes.
func WriteGateFromEnv() bool {
	v := os.Getenv("LOOMWORK_WRITE_GATE")
	return v == "1" || v == "true"
}

// Kind is the record type discriminator.
type Kind string

const (
	KindPreference Kind = "preference"
	KindEpisode    Kind = "episode"
	KindArtifact   Kind = "artifact"
	KindBelief     Kind = "belief"
	KindFailure    Kind = "failure"
)

// Sensitivity level — affects encryption, logging, and consent defaults.
type Sensitivity string

const (
	SensLow    Sensitivity = "low"
	SensMedium Sensitivity = "medium"
	SensHigh   Sensitivity = "high"
)

// Status of a record in the propose→approve lifecycle.
type Status string

const (
	StatusPending    Status = "pending"    // proposed by agent, awaiting review
	StatusActive     Status = "active"     // approved, durable
	StatusSuperseded Status = "superseded" // replaced by a newer record
	StatusRevoked    Status = "revoked"    // content deleted (cascades to derivatives)
	StatusRejected   Status = "rejected"   // proposal declined by a reviewer; content deleted
)

// Retention is the retention policy for a record.
type Retention struct {
	Mode       string `json:"mode"`                 // "until_revoked" | "ttl"
	TTLSeconds int64  `json:"ttlSeconds,omitempty"` // for "ttl" mode
	ExpiresAt  string `json:"expiresAt,omitempty"`  // computed on write
	// PendingTTL marks an expiry imposed by the runtime on an unapproved
	// proposal. The writer's own retention is kept in Requested* and takes
	// effect only on approval (see clearPendingTTL).
	PendingTTL          bool   `json:"pendingTtl,omitempty"`
	RequestedMode       string `json:"requestedMode,omitempty"`
	RequestedTTLSeconds int64  `json:"requestedTtlSeconds,omitempty"`
}

// Provenance traces a record back to its origin.
type Provenance struct {
	WriterAgentID string `json:"writerAgentId"` // did:key:... of the agent that wrote it
	WriterACI     string `json:"writerAci"`     // aci identifier
	WrittenAt     string `json:"writtenAt"`
	Source        string `json:"source"`              // "user_input" | "agent_inferred" | "imported" | "derived"
	ParentID      string `json:"parentId,omitempty"`  // if derived, the parent record ID
	SourceURI     string `json:"sourceUri,omitempty"` // for imported: file://, https://
}

// ConsentScope declares which agents may see a record.
type ConsentScope struct {
	// AllowedAgents is a list of agent IDs (did:key:...) allowed to read.
	// Empty list = private to writer + user only.
	AllowedAgents []string `json:"allowedAgents,omitempty"`
	// AllowedScopes is a list of capability scopes (e.g. "research", "coding").
	AllowedScopes []string `json:"allowedScopes,omitempty"`
	// Public means any agent the user has authorized can see it.
	Public bool `json:"public,omitempty"`
}

// Record is the common envelope for all typed memory records.
type Record struct {
	ID          string       `json:"id"`
	Kind        Kind         `json:"kind"`
	Status      Status       `json:"status"`
	Sensitivity Sensitivity  `json:"sensitivity"`
	Provenance  Provenance   `json:"provenance"`
	Consent     ConsentScope `json:"consent"`
	Retention   Retention    `json:"retention"`
	Derivatives []string     `json:"derivatives,omitempty"` // child record IDs
	CreatedAt   string       `json:"createdAt"`
	UpdatedAt   string       `json:"updatedAt"`
	RevokedAt   string       `json:"revokedAt,omitempty"`

	// Typed payload — only one of these is populated per record.
	Preference *Preference `json:"preference,omitempty"`
	Episode    *Episode    `json:"episode,omitempty"`
	Artifact   *Artifact   `json:"artifact,omitempty"`
	Belief     *Belief     `json:"belief,omitempty"`
	Failure    *Failure    `json:"failure,omitempty"`
}

// Preference: a user-stated or agent-inferred preference.
type Preference struct {
	Key        string  `json:"key"`        // e.g. "code_style.indent"
	Value      string  `json:"value"`      // e.g. "tabs"
	Source     string  `json:"source"`     // "user_stated" | "inferred"
	Confidence float64 `json:"confidence"` // 0.0–1.0 (1.0 for user-stated)
}

// Episode: a timestamped interaction event.
type Episode struct {
	Timestamp string   `json:"timestamp"` // RFC 3339
	Summary   string   `json:"summary"`
	Inputs    []string `json:"inputs,omitempty"`  // record IDs of input artifacts
	Outputs   []string `json:"outputs,omitempty"` // record IDs of output artifacts
	Outcome   string   `json:"outcome,omitempty"` // "success" | "failure" | "partial"
}

// Artifact: a durable output.
type Artifact struct {
	Name       string `json:"name"`
	Mime       string `json:"mime"`
	Digest     string `json:"digest"`               // sha256:...
	ContentRef string `json:"contentRef,omitempty"` // path or URI to the actual content
	CreatedBy  string `json:"createdBy"`            // agent ID
}

// Belief: an inferred claim about the world (cited).
type Belief struct {
	Claim        string   `json:"claim"`    // e.g. "User works at Acme Corp"
	Evidence     []string `json:"evidence"` // record IDs that support this belief
	Confidence   float64  `json:"confidence"`
	LastVerified string   `json:"lastVerified,omitempty"` // RFC 3339
	SupersededBy string   `json:"supersededBy,omitempty"` // record ID that supersedes this
}

// Failure: a known-bad-path warning (first-class failure memory).
type Failure struct {
	Pattern     string `json:"pattern"` // e.g. "using library X for task Y"
	WhatFailed  string `json:"whatFailed"`
	Why         string `json:"why"`
	Avoidance   string `json:"avoidance"`   // what to do instead
	Occurrences int    `json:"occurrences"` // how many times observed
}

// NewRecord constructs a record with sensible defaults.
func NewRecord(kind Kind, sens Sensitivity, prov Provenance) *Record {
	now := time.Now().UTC().Format(time.RFC3339)
	return &Record{
		ID:          newID("mem"),
		Kind:        kind,
		Status:      StatusPending, // default: agents propose, humans/policy decide
		Sensitivity: sens,
		Provenance:  prov,
		Consent: ConsentScope{
			AllowedAgents: []string{prov.WriterAgentID}, // default: writer only
		},
		Retention: Retention{Mode: "until_revoked"},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// IsExpired returns true if a TTL record has passed its expiry.
func (r *Record) IsExpired() bool {
	if r.Retention.Mode != "ttl" || r.Retention.ExpiresAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, r.Retention.ExpiresAt)
	if err != nil {
		return false
	}
	return time.Now().UTC().After(t)
}

// IsVisibleTo returns true if the record is visible to the given agent.
// Revoked, rejected and expired records are never visible. A pending record is
// visible only to its writer (a reviewer View also sees it; see View.sees).
// High-sensitivity records additionally require the reviewer identity ("user") or the "sensitive" scope. Otherwise an active
// record is visible per its consent scope.
func (r *Record) IsVisibleTo(agentID string, agentScopes []string) bool {
	if r.Status == StatusRevoked || r.Status == StatusRejected || r.IsExpired() {
		return false
	}
	if r.Status == StatusPending {
		return r.Provenance.WriterAgentID == agentID
	}
	if r.Sensitivity == SensHigh && agentID != "user" && !hasScope(agentScopes, "sensitive") {
		return false
	}
	if r.Consent.Public {
		return true
	}
	for _, a := range r.Consent.AllowedAgents {
		if a == agentID {
			return true
		}
	}
	for _, s := range r.Consent.AllowedScopes {
		if hasScope(agentScopes, s) {
			return true
		}
	}
	return false
}

func hasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}

// newID returns a prefixed, time-ordered ID with a random suffix.
func newID(prefix string) string {
	return prefix + "_" + time.Now().UTC().Format("20060102150405") + "_" + randHex(8)
}

// randHex returns n random hex characters from the system CSPRNG.
func randHex(n int) string {
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system CSPRNG failing is unrecoverable
	}
	return hex.EncodeToString(b)[:n]
}
