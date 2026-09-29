package amp

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"loomwork.dev/loomwork/internal/aci"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CreditLogger is the v0.1 "logging mode" credit implementation.
//
// Per docs/credit-experimental.md: a v0.1 runtime MAY implement amp/credit
// as a pure log writer. Every delegation emits a dual-signed receipt to a
// local log file. No settlement attempted.
//
// This preserves the audit trail without the Lightning dependency. A future
// v0.2 settler can read the log and redeem receipts without re-signing.
type CreditLogger struct {
	mu      sync.Mutex
	logPath string
	enabled bool
}

// NewCreditLogger constructs a logger writing to logPath.
// If logPath is empty, credit logging is disabled (Off mode).
func NewCreditLogger(logPath string) *CreditLogger {
	return &CreditLogger{
		logPath: logPath,
		enabled: logPath != "",
	}
}

// LogReceipt writes a dual-signed credit receipt to the log file as JSONL.
// Returns an error if logging is disabled or the write fails.
func (cl *CreditLogger) LogReceipt(r *CreditReceipt) error {
	if !cl.enabled {
		return fmt.Errorf("credit logging is disabled (Off mode)")
	}
	cl.mu.Lock()
	defer cl.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(cl.logPath), 0o700); err != nil {
		return err
	}

	f, err := os.OpenFile(cl.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	r.LoggedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = f.Write(data)
	return err
}

// CreditReceipt is the dual-signed receipt format (stable across all credit modes).
//
// Per docs/credit-experimental.md: the receipt format is identical in logging
// mode and settled mode. A log full of these receipts can be redeemed by a
// future Lightning settler without re-signing.
type CreditReceipt struct {
	ReceiptID string `json:"receiptId"`
	TaskID    string `json:"taskId"`
	Amount    struct {
		Currency string `json:"currency"`
		Value    int64  `json:"value"`
	} `json:"amount"`
	Breakdown struct {
		Tokens      int `json:"tokens"`
		ToolCalls   int `json:"toolCalls"`
		WallSeconds int `json:"wallSeconds"`
	} `json:"breakdown"`
	Payer    string `json:"payer"`
	Payee    string `json:"payee"`
	IssuedAt string `json:"issuedAt"`
	PayerSig string `json:"payerSig"`
	PayeeSig string `json:"payeeSig,omitempty"`
	LoggedAt string `json:"loggedAt,omitempty"`
}

// ReadCreditLog reads all receipts from the log file. Used for audit/reconciliation.
func ReadCreditLog(logPath string) ([]*CreditReceipt, error) {
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*CreditReceipt
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var r CreditReceipt
		if err := json.Unmarshal(line, &r); err != nil {
			continue // skip malformed lines
		}
		out = append(out, &r)
	}
	return out, nil
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

func (r *CreditReceipt) payload() ([]byte, error) {
	cp := *r
	cp.PayerSig, cp.PayeeSig, cp.LoggedAt = "", "", ""
	return json.Marshal(cp)
}

// SignReceipt adds the payer's or payee's Ed25519 signature to the receipt.
func SignReceipt(r *CreditReceipt, sk *aci.SigningKey, asPayer bool) error {
	p, err := r.payload()
	if err != nil {
		return err
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(sk.Priv, p))
	if asPayer {
		r.PayerSig = sig
	} else {
		r.PayeeSig = sig
	}
	return nil
}

// VerifyReceipt checks the payer signature (required) and the payee signature
// (when present) against the given keys.
func VerifyReceipt(r *CreditReceipt, payer, payee *aci.VerifyingKey) error {
	p, err := r.payload()
	if err != nil {
		return err
	}
	check := func(sig string, k *aci.VerifyingKey, who string) error {
		raw, err := base64.StdEncoding.DecodeString(sig)
		if err != nil || k == nil || !ed25519.Verify(k.Pub, p, raw) {
			return fmt.Errorf("%s signature invalid", who)
		}
		return nil
	}
	if err := check(r.PayerSig, payer, "payer"); err != nil {
		return err
	}
	if r.PayeeSig != "" {
		return check(r.PayeeSig, payee, "payee")
	}
	return nil
}
