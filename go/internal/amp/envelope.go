// Package amp implements the v0.1 subset of the Agent Mesh Protocol: the
// envelope, capability tokens, signed provenance, amp/delegate and amp/report
// over a newline-delimited JSON transport, and a credit receipt log.
package amp

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loomwork.dev/loomwork/internal/aci"
)

// Envelope is the AMP message envelope: JSON-RPC 2.0 + AMP extensions.
type Envelope struct {
	JSONRPC      string            `json:"jsonrpc"`
	ID           string            `json:"id"`
	Method       string            `json:"method,omitempty"`
	Params       json.RawMessage   `json:"params,omitempty"`
	Result       json.RawMessage   `json:"result,omitempty"`
	Error        *ErrorObject      `json:"error,omitempty"`
	AMP          AMPMetadata       `json:"amp"`
	Capabilities []CapabilityToken `json:"capabilities"`
	Provenance   *Provenance       `json:"provenance,omitempty"`
}

// AMPMetadata is the `amp` field of the envelope.
type AMPMetadata struct {
	Version   string `json:"version"`
	TraceID   string `json:"traceId"`
	SessionID string `json:"sessionId,omitempty"`
	TTL       int    `json:"ttl"`
}

// Provenance traces a request back to its originating ACI.
type Provenance struct {
	ACI       string `json:"aci"`
	ACIDigest string `json:"aciDigest"`
	Runtime   string `json:"runtime"`
	// Signer is the key ID of the ACI signer; Signature is that key's Ed25519
	// signature over "aci|aciDigest|runtime".
	Signer    string `json:"signer,omitempty"`
	Signature string `json:"signature,omitempty"`
}

// ErrorObject is the JSON-RPC 2.0 error object.
type ErrorObject struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// CapabilityToken is a signed, time-boxed grant to invoke a skill.
type CapabilityToken struct {
	ID        string         `json:"id"`
	Skill     string         `json:"skill"`
	Scope     map[string]any `json:"scope,omitempty"`
	IssuedBy  string         `json:"issuedBy"`
	IssuedAt  string         `json:"issuedAt"`
	ExpiresAt string         `json:"expiresAt"`
	Signature string         `json:"signature,omitempty"`
}

// NewRequest constructs an AMP request envelope.
func NewRequest(method string, params any, prov *Provenance) (*Envelope, error) {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return &Envelope{
		JSONRPC: "2.0",
		ID:      newID("req"),
		Method:  method,
		Params:  paramsJSON,
		AMP: AMPMetadata{
			Version: "0.1",
			TraceID: newID("trace"),
			TTL:     30,
		},
		Capabilities: []CapabilityToken{},
		Provenance:   prov,
	}, nil
}

// NewResponse constructs a success response.
func NewResponse(reqID string, result any, traceID string) (*Envelope, error) {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &Envelope{
		JSONRPC: "2.0",
		ID:      reqID,
		Result:  resultJSON,
		AMP:     AMPMetadata{Version: "0.1", TraceID: traceID, TTL: 30},
	}, nil
}

// NewError constructs an error response.
func NewError(reqID string, code int, message string, traceID string) *Envelope {
	return &Envelope{
		JSONRPC: "2.0",
		ID:      reqID,
		Error:   &ErrorObject{Code: code, Message: message},
		AMP:     AMPMetadata{Version: "0.1", TraceID: traceID, TTL: 30},
	}
}

// Marshal returns the JSON encoding for wire transmission.
func (e *Envelope) Marshal() ([]byte, error) {
	return json.Marshal(e)
}

// MaxMessageBytes bounds a single wire message.
const MaxMessageBytes = 1 << 20

// UnmarshalEnvelope parses and validates a wire message.
func UnmarshalEnvelope(data []byte) (*Envelope, error) {
	if len(data) > MaxMessageBytes {
		return nil, fmt.Errorf("PROTOCOL_VIOLATION: message exceeds %d bytes", MaxMessageBytes)
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("PROTOCOL_VIOLATION: %w", err)
	}
	if env.JSONRPC != "2.0" {
		return nil, fmt.Errorf("PROTOCOL_VIOLATION: jsonrpc must be \"2.0\"")
	}
	if strings.HasPrefix(env.Method, "amp/") {
		if env.AMP.Version == "" {
			return nil, fmt.Errorf("PROTOCOL_VIOLATION: AMP-method request missing amp.version")
		}
		if env.AMP.TraceID == "" {
			return nil, fmt.Errorf("PROTOCOL_VIOLATION: AMP-method request missing amp.traceId")
		}
		if env.Capabilities == nil {
			return nil, fmt.Errorf("PROTOCOL_VIOLATION: AMP-method request missing capabilities field")
		}
		if env.Provenance == nil {
			return nil, fmt.Errorf("PROTOCOL_VIOLATION: AMP-method request missing provenance field")
		}
	}
	return &env, nil
}

func (p *Provenance) payload() []byte {
	return []byte(p.ACI + "|" + p.ACIDigest + "|" + p.Runtime)
}

// ProvenanceFor builds signed provenance for an ACI. rawACI must be the exact
// bytes of the .aci file, so the digest matches what a peer would hash. The
// signature is made with the key that signed the ACI.
func ProvenanceFor(archive *aci.Archive, rawACI []byte, sk *aci.SigningKey) *Provenance {
	p := &Provenance{
		ACI:       "loomwork.dev/" + archive.Manifest.Metadata.Name + "@" + archive.Manifest.Metadata.Version,
		ACIDigest: aci.Sha256Bytes(rawACI),
		Runtime:   "loomwork-go/0.1.2",
		Signer:    sk.KeyID,
	}
	p.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(sk.Priv, p.payload()))
	return p
}

// VerifyProvenance checks the signature against the trusted keys and returns
// the key that made it.
func VerifyProvenance(p *Provenance, trusted []*aci.VerifyingKey) (*aci.VerifyingKey, error) {
	if p == nil || p.Signature == "" {
		return nil, fmt.Errorf("provenance is unsigned")
	}
	sig, err := base64.StdEncoding.DecodeString(p.Signature)
	if err != nil {
		return nil, fmt.Errorf("provenance signature: %w", err)
	}
	for _, k := range trusted {
		if k.KeyID == p.Signer && ed25519.Verify(k.Pub, p.payload(), sig) {
			return k, nil
		}
	}
	return nil, fmt.Errorf("provenance is not signed by a trusted key")
}

// SignCapability signs a token with the issuer's key.
func SignCapability(t *CapabilityToken, sk *aci.SigningKey) error {
	t.IssuedBy = sk.KeyID
	p, err := t.payload()
	if err != nil {
		return err
	}
	t.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(sk.Priv, p))
	return nil
}

func (t *CapabilityToken) payload() ([]byte, error) {
	cp := *t
	cp.Signature = ""
	return json.Marshal(cp)
}

// VerifyCapability checks that a token authorizes skill: issued by a trusted
// key, correctly signed, unexpired, and for exactly this skill.
func VerifyCapability(t *CapabilityToken, skill string, trusted []*aci.VerifyingKey, now time.Time) error {
	if t.Skill != skill {
		return fmt.Errorf("token is for skill %q, not %q", t.Skill, skill)
	}
	exp, err := time.Parse(time.RFC3339, t.ExpiresAt)
	if err != nil {
		return fmt.Errorf("token has an invalid expiry")
	}
	if !now.Before(exp) {
		return fmt.Errorf("token expired at %s", t.ExpiresAt)
	}
	if iss, err := time.Parse(time.RFC3339, t.IssuedAt); err != nil || now.Before(iss.Add(-time.Minute)) {
		return fmt.Errorf("token issue time is invalid or in the future")
	}
	sig, err := base64.StdEncoding.DecodeString(t.Signature)
	if err != nil || t.Signature == "" {
		return fmt.Errorf("token is unsigned")
	}
	p, err := t.payload()
	if err != nil {
		return err
	}
	for _, k := range trusted {
		if k.KeyID == t.IssuedBy && ed25519.Verify(k.Pub, p, sig) {
			return nil
		}
	}
	return fmt.Errorf("token is not signed by a trusted key")
}

// Standard AMP error codes (subset for MVP).
const (
	CodeUnauthenticated   = 401
	CodeCapabilityDenied  = 403
	CodeMethodNotFound    = -32601
	CodeInternal          = 502
	CodeProtocolViolation = 500
	CodeVersionMismatch   = 501
	CodeTaskNotFound      = 300
	CodeBudgetExceeded    = 302
)

// DelegateParams is the params for amp/delegate (§5.5.1).
type DelegateParams struct {
	TaskID   string         `json:"taskId"`
	Spec     DelegateSpec   `json:"spec"`
	Deadline string         `json:"deadline,omitempty"`
	Budget   map[string]int `json:"budget,omitempty"`
}

// DelegateSpec is the task specification.
type DelegateSpec struct {
	// Skill names the skill to run; empty means "answer_question".
	Skill           string         `json:"skill,omitempty"`
	Intent          string         `json:"intent"`
	Inputs          map[string]any `json:"inputs"`
	ExpectedOutputs []string       `json:"expectedOutputs,omitempty"`
	Format          string         `json:"format,omitempty"`
}

// DelegateResult is the response from amp/delegate.
type DelegateResult struct {
	Accepted            bool   `json:"accepted"`
	EstimatedCompletion string `json:"estimatedCompletion,omitempty"`
	Reason              string `json:"reason,omitempty"`
}

// ReportParams is the params for amp/report (§5.5.3).
type ReportParams struct {
	TaskID    string           `json:"taskId"`
	Status    string           `json:"status"` // pending, partial, complete, failed
	Artifacts []ReportArtifact `json:"artifacts,omitempty"`
	Usage     map[string]int   `json:"usage,omitempty"`
	Error     string           `json:"error,omitempty"`
}

// ReportArtifact is a single output artifact.
type ReportArtifact struct {
	Name   string `json:"name"`
	Mime   string `json:"mime"`
	Digest string `json:"digest"`
	// Inline carries small text results directly.
	Inline string `json:"inline,omitempty"`
}

// NewID returns a unique, prefixed ID: a timestamp plus random bytes, so two
// IDs made in the same nanosecond still differ and IDs are not guessable.
func NewID(prefix string) string { return newID(prefix) }

func newID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s_%d_%s", prefix, time.Now().UnixNano(), hex.EncodeToString(b))
}
