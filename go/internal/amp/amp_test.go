package amp

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"loomwork.dev/loomwork/internal/aci"
)

func TestShortMethodDoesNotPanic(t *testing.T) {
	for _, m := range []string{"", "a", "ab", "abc", "amp"} {
		msg := `{"jsonrpc":"2.0","id":"1","method":"` + m + `"}`
		if _, err := UnmarshalEnvelope([]byte(msg)); err != nil && m != "" && strings.HasPrefix(m, "amp/") {
			t.Errorf("%q: %v", m, err)
		}
	}
	if _, err := UnmarshalEnvelope([]byte(`{"jsonrpc":"1.0","id":"1"}`)); err == nil {
		t.Error("wrong jsonrpc version must be rejected")
	}
}

func TestOversizedMessageRejected(t *testing.T) {
	big := `{"jsonrpc":"2.0","id":"` + strings.Repeat("a", MaxMessageBytes) + `"}`
	if _, err := UnmarshalEnvelope([]byte(big)); err == nil {
		t.Fatal("oversized message must be rejected")
	}
	tr := NewLineTransport(strings.NewReader(big+"\n"), io.Discard, nil)
	if _, err := tr.Recv(context.Background()); err == nil {
		t.Fatal("oversized line must be rejected by the transport")
	}
}

func TestRecvTimeoutDoesNotLoseData(t *testing.T) {
	pr, pw := io.Pipe()
	tr := NewLineTransport(pr, io.Discard, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := tr.Recv(ctx); err == nil {
		t.Fatal("expected timeout")
	}
	go pw.Write([]byte(`{"jsonrpc":"2.0","id":"7","result":{}}` + "\n"))
	env, err := tr.Recv(context.Background())
	if err != nil || env.ID != "7" {
		t.Fatalf("message after a timeout must still arrive: %v %v", env, err)
	}
}

func TestCapabilityTokens(t *testing.T) {
	issuer, _ := aci.GenerateSigningKey()
	stranger, _ := aci.GenerateSigningKey()
	trusted := []*aci.VerifyingKey{issuer.VerifyingKey()}
	now := time.Now().UTC()
	mk := func(skill string, ttl time.Duration, sk *aci.SigningKey) *CapabilityToken {
		tok := &CapabilityToken{ID: "c", Skill: skill, IssuedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(ttl).Format(time.RFC3339)}
		SignCapability(tok, sk)
		return tok
	}
	if err := VerifyCapability(mk("s", time.Hour, issuer), "s", trusted, now); err != nil {
		t.Errorf("valid token rejected: %v", err)
	}
	if VerifyCapability(mk("s", time.Hour, issuer), "other", trusted, now) == nil {
		t.Error("token for another skill accepted")
	}
	if VerifyCapability(mk("s", -time.Minute, issuer), "s", trusted, now) == nil {
		t.Error("expired token accepted")
	}
	if VerifyCapability(mk("s", time.Hour, stranger), "s", trusted, now) == nil {
		t.Error("token from an untrusted key accepted")
	}
	tampered := mk("s", time.Hour, issuer)
	tampered.ExpiresAt = now.Add(100 * time.Hour).Format(time.RFC3339)
	if VerifyCapability(tampered, "s", trusted, now) == nil {
		t.Error("tampered token accepted")
	}
	unsigned := &CapabilityToken{Skill: "s", IssuedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}
	if VerifyCapability(unsigned, "s", trusted, now) == nil {
		t.Error("unsigned token accepted")
	}
}

func TestProvenanceSignature(t *testing.T) {
	sk, _ := aci.GenerateSigningKey()
	other, _ := aci.GenerateSigningKey()
	p := &Provenance{ACI: "a@v1", ACIDigest: "sha256:x", Runtime: "r", Signer: sk.KeyID}
	if _, err := VerifyProvenance(p, []*aci.VerifyingKey{sk.VerifyingKey()}); err == nil {
		t.Fatal("unsigned provenance accepted")
	}
	// sign by hand through the exported path
	arch := &aci.Archive{Manifest: &aci.Manifest{Metadata: aci.Metadata{Name: "a", Version: "v1"}}}
	p = ProvenanceFor(arch, []byte("bytes"), sk)
	if _, err := VerifyProvenance(p, []*aci.VerifyingKey{sk.VerifyingKey()}); err != nil {
		t.Fatalf("valid provenance rejected: %v", err)
	}
	if _, err := VerifyProvenance(p, []*aci.VerifyingKey{other.VerifyingKey()}); err == nil {
		t.Fatal("provenance accepted without a trusted signer")
	}
	p.ACIDigest = "sha256:forged"
	if _, err := VerifyProvenance(p, []*aci.VerifyingKey{sk.VerifyingKey()}); err == nil {
		t.Fatal("altered provenance accepted")
	}
}

// pipePair connects two LineTransports.
func pipePair() (*LineTransport, *LineTransport) {
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	return NewLineTransport(ar, aw, nil), NewLineTransport(br, bw, nil)
}

func TestDelegateEndToEnd(t *testing.T) {
	caller, _ := aci.GenerateSigningKey()
	stranger, _ := aci.GenerateSigningKey()
	callerT, calleeT := pipePair()
	trusted := []*aci.VerifyingKey{caller.VerifyingKey()}

	srv := &Server{Transport: calleeT, Trusted: trusted, Skills: []string{"answer_question"},
		Handler: func(s DelegateSpec) (string, error) { return "echo: " + s.Intent, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx)

	arch := &aci.Archive{Manifest: &aci.Manifest{Metadata: aci.Metadata{Name: "a", Version: "v1"}}}
	prov := ProvenanceFor(arch, []byte("aci"), caller)
	now := time.Now().UTC()
	tok := CapabilityToken{ID: "c", Skill: "answer_question", IssuedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}
	SignCapability(&tok, caller)

	client := NewClient(callerT)
	client.SetTimeout(3 * time.Second)
	res, err := client.Delegate(&DelegateParams{TaskID: "t1", Spec: DelegateSpec{Intent: "hello"}}, prov, []CapabilityToken{tok})
	if err != nil || !res.Accepted {
		t.Fatalf("delegate failed: %v %+v", err, res)
	}
	rep, err := client.AwaitReport(3 * time.Second)
	if err != nil || rep.Status != "complete" || len(rep.Artifacts) != 1 || rep.Artifacts[0].Inline != "echo: hello" {
		t.Fatalf("bad report: %v %+v", err, rep)
	}

	// no token
	if _, err := client.Delegate(&DelegateParams{TaskID: "t2", Spec: DelegateSpec{Intent: "x"}}, prov, nil); err == nil ||
		!strings.Contains(err.Error(), "403") {
		t.Errorf("call without a token must be denied with 403, got %v", err)
	}
	// unsigned provenance / stranger's provenance
	bad := ProvenanceFor(arch, []byte("aci"), stranger)
	if _, err := client.Delegate(&DelegateParams{TaskID: "t3", Spec: DelegateSpec{Intent: "x"}}, bad, []CapabilityToken{tok}); err == nil ||
		!strings.Contains(err.Error(), "401") {
		t.Errorf("untrusted caller must be rejected with 401, got %v", err)
	}
	// unknown skill
	tok2 := CapabilityToken{ID: "c2", Skill: "rm_rf", IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt}
	SignCapability(&tok2, caller)
	if _, err := client.Delegate(&DelegateParams{TaskID: "t4", Spec: DelegateSpec{Skill: "rm_rf", Intent: "x"}}, prov, []CapabilityToken{tok2}); err == nil ||
		!strings.Contains(err.Error(), "300") {
		t.Errorf("unknown skill must be refused, got %v", err)
	}
}

func TestReceiptSignatures(t *testing.T) {
	payer, _ := aci.GenerateSigningKey()
	payee, _ := aci.GenerateSigningKey()
	r := &CreditReceipt{ReceiptID: "r1", TaskID: "t", Payer: "a", Payee: "b", IssuedAt: "now"}
	r.Amount.Currency, r.Amount.Value = "credit", 5
	if VerifyReceipt(r, payer.VerifyingKey(), payee.VerifyingKey()) == nil {
		t.Fatal("unsigned receipt accepted")
	}
	SignReceipt(r, payer, true)
	SignReceipt(r, payee, false)
	if err := VerifyReceipt(r, payer.VerifyingKey(), payee.VerifyingKey()); err != nil {
		t.Fatal(err)
	}
	r.Amount.Value = 500
	if VerifyReceipt(r, payer.VerifyingKey(), payee.VerifyingKey()) == nil {
		t.Fatal("altered receipt accepted")
	}
	_ = bytes.Buffer{}
}
