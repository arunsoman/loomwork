package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(filepath.Join(dir, "test.db"), "test-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestRevocationCascade checks that revoking one record removes its embedding
// and everything derived from it.
//
// Setup:
//  1. raw_user_input (artifact)
//  2. summary (episode, derived from 1)
//  3. embedding (stored separately for record 1)
//  4. belief (derived from 2, cites it as evidence)
//  5. downstream_decision (artifact, derived from 4)
//
// Action: revoke record 1.
// Expect: 1, 2, 4, 5 all marked revoked. Embeddings for all of them deleted.
//
//	A view belonging to any agent returns NONE of these records.
func TestRevocationCascade(t *testing.T) {
	s := openTestStore(t)
	view := NewReviewerView(s)

	// 1. raw_user_input
	rawInput := NewRecord(KindArtifact, SensLow, Provenance{
		WriterAgentID: "agent_a", WriterACI: "test@v0.1", Source: "user_input",
	})
	rawInput.Artifact = &Artifact{Name: "notes.md", Mime: "text/markdown", Digest: "sha256:abc", CreatedBy: "agent_a"}
	if err := view.Propose(rawInput); err != nil {
		t.Fatal(err)
	}
	if err := view.Approve(rawInput.ID); err != nil {
		t.Fatal(err)
	}
	// Write an embedding for it
	if err := s.WriteEmbedding(rawInput.ID, []float32{0.1, 0.2, 0.3}, "test-model"); err != nil {
		t.Fatal(err)
	}

	// 2. summary (derived from 1)
	summary := NewRecord(KindEpisode, SensLow, Provenance{
		WriterAgentID: "agent_a", WriterACI: "test@v0.1", Source: "agent_inferred",
		ParentID: rawInput.ID,
	})
	summary.Episode = &Episode{Summary: "User said X about Y", Outcome: "success"}
	if err := view.Propose(summary); err != nil {
		t.Fatal(err)
	}
	if err := view.Approve(summary.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteEmbedding(summary.ID, []float32{0.4, 0.5, 0.6}, "test-model"); err != nil {
		t.Fatal(err)
	}

	// 4. belief (derived from 2)
	belief := NewRecord(KindBelief, SensMedium, Provenance{
		WriterAgentID: "agent_a", WriterACI: "test@v0.1", Source: "agent_inferred",
		ParentID: summary.ID,
	})
	belief.Belief = &Belief{
		Claim:      "User cares about Y",
		Evidence:   []string{summary.ID},
		Confidence: 0.85,
	}
	if err := view.Propose(belief); err != nil {
		t.Fatal(err)
	}
	if err := view.Approve(belief.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteEmbedding(belief.ID, []float32{0.7, 0.8, 0.9}, "test-model"); err != nil {
		t.Fatal(err)
	}

	// 5. downstream_decision (derived from belief 4)
	decision := NewRecord(KindArtifact, SensMedium, Provenance{
		WriterAgentID: "agent_b", WriterACI: "test@v0.1", Source: "agent_inferred",
		ParentID: belief.ID,
	})
	decision.Artifact = &Artifact{Name: "decision.md", Mime: "text/markdown", Digest: "sha256:xyz", CreatedBy: "agent_b"}
	decision.Consent = ConsentScope{AllowedAgents: []string{"agent_a", "agent_b"}}
	if err := view.Propose(decision); err != nil {
		t.Fatal(err)
	}
	if err := view.Approve(decision.ID); err != nil {
		t.Fatal(err)
	}

	// Sanity check: all 4 records are active and visible
	agentAView := NewView(s, "agent_a", []string{})
	for _, id := range []string{rawInput.ID, summary.ID, belief.ID, decision.ID} {
		r, err := agentAView.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if r == nil {
			t.Fatalf("record %s should be visible before revocation", id)
		}
		if r.Status != StatusActive {
			t.Fatalf("record %s should be active, got %s", id, r.Status)
		}
	}

	// Verify embeddings exist
	for _, id := range []string{rawInput.ID, summary.ID, belief.ID} {
		emb, _, err := s.GetEmbedding(id)
		if err != nil || emb == nil {
			t.Fatalf("embedding for %s should exist before revocation", id)
		}
	}

	// === ACTION: revoke the root record ===
	result, err := view.Revoke(rawInput.ID)
	if err != nil {
		t.Fatal(err)
	}

	// === ASSERTIONS ===

	// 1. All 4 records should be marked revoked
	if len(result.RevokedRecords) != 4 {
		t.Fatalf("expected 4 revoked records, got %d: %v", len(result.RevokedRecords), result.RevokedRecords)
	}
	revokedSet := map[string]bool{}
	for _, id := range result.RevokedRecords {
		revokedSet[id] = true
	}
	for _, id := range []string{rawInput.ID, summary.ID, belief.ID, decision.ID} {
		if !revokedSet[id] {
			t.Errorf("expected %s in revoked set", id)
		}
	}

	// 2. All embeddings should be deleted (3 were written)
	// (We wrote embeddings for rawInput, summary, belief — 3 total)
	if result.DeletedEmbeddings < 3 {
		t.Errorf("expected at least 3 deleted embeddings, got %d", result.DeletedEmbeddings)
	}
	for _, id := range []string{rawInput.ID, summary.ID, belief.ID} {
		emb, _, _ := s.GetEmbedding(id)
		if emb != nil {
			t.Errorf("embedding for %s should be deleted after revocation", id)
		}
	}

	// 3. Cascade depth should be 3 (root → summary → belief → decision)
	if result.Depth != 3 {
		t.Errorf("expected cascade depth 3, got %d", result.Depth)
	}

	// 4. NONE of the records should be visible to any agent view
	for _, id := range []string{rawInput.ID, summary.ID, belief.ID, decision.ID} {
		r, _ := agentAView.Get(id)
		if r != nil {
			t.Errorf("revoked record %s should not be visible to agent_a view", id)
		}
	}

	// 5. The records still exist in the DB (marked revoked, not deleted — for audit)
	r, err := s.Get(rawInput.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r == nil {
		t.Fatal("revoked record should still exist in DB (marked revoked, not deleted)")
	}
	if r.Status != StatusRevoked {
		t.Errorf("expected status revoked, got %s", r.Status)
	}
}

// TestProposeApproveFlow tests the "agents propose, humans decide" flow.
func TestProposeApproveFlow(t *testing.T) {
	s := openTestStore(t)

	// Agent view (cannot approve)
	agentView := NewView(s, "agent_a", []string{})

	// Reviewer view (can approve)
	reviewerView := NewReviewerView(s)

	// Agent proposes a belief
	r := NewRecord(KindBelief, SensLow, Provenance{
		WriterAgentID: "agent_a", WriterACI: "test@v0.1", Source: "agent_inferred",
	})
	r.Belief = &Belief{Claim: "User likes Go", Confidence: 0.9}
	if err := agentView.Propose(r); err != nil {
		t.Fatal(err)
	}

	// Another agent should NOT see the pending record
	otherAgent := NewView(s, "agent_b", []string{})
	got, err := otherAgent.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("pending record should not be visible to other agents")
	}

	// Writer agent CAN see their own pending record
	got, err = agentView.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Error("writer should see their own pending record")
	}

	// Reviewer approves
	if err := reviewerView.Approve(r.ID); err != nil {
		t.Fatal(err)
	}

	// Now the record is active — but still only visible to agent_a (consent default)
	got, _ = agentView.Get(r.ID)
	if got == nil || got.Status != StatusActive {
		t.Error("record should be active and visible to writer after approval")
	}

	// Reviewer rejects another proposed record
	r2 := NewRecord(KindBelief, SensLow, Provenance{
		WriterAgentID: "agent_a", WriterACI: "test@v0.1", Source: "agent_inferred",
	})
	r2.Belief = &Belief{Claim: "Wrong claim", Confidence: 0.1}
	_ = agentView.Propose(r2)
	if err := reviewerView.Reject(r2.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = agentView.Get(r2.ID)
	if got != nil && got.Status != StatusRevoked {
		t.Error("rejected record should be revoked")
	}
}

// TestConsentScope tests that records are only visible to allowed agents.
func TestConsentScope(t *testing.T) {
	s := openTestStore(t)
	reviewer := NewReviewerView(s)

	// Create a high-sensitivity record visible only to agent_a
	r := NewRecord(KindPreference, SensHigh, Provenance{
		WriterAgentID: "agent_a", WriterACI: "test@v0.1", Source: "user_input",
	})
	r.Preference = &Preference{Key: "salary", Value: "secret", Source: "user_stated", Confidence: 1.0}
	r.Consent = ConsentScope{AllowedAgents: []string{"agent_a"}}
	if err := reviewer.Propose(r); err != nil {
		t.Fatal(err)
	}
	if err := reviewer.Approve(r.ID); err != nil {
		t.Fatal(err)
	}

	// agent_a can see it
	viewA := NewView(s, "agent_a", []string{})
	got, _ := viewA.Get(r.ID)
	if got != nil {
		t.Error("high-sensitivity record needs the sensitive scope, even for its consented agent")
	}
	viewA = NewView(s, "agent_a", []string{"sensitive"})
	got, _ = viewA.Get(r.ID)
	if got == nil {
		t.Error("agent_a should see their own record with the sensitive scope")
	}

	// agent_b cannot
	viewB := NewView(s, "agent_b", []string{"sensitive"})
	got, _ = viewB.Get(r.ID)
	if got != nil {
		t.Error("agent_b should not see agent_a's private record")
	}

	// Make it public — re-fetch first so we have the current status
	r, _ = s.Get(r.ID)
	r.Consent = ConsentScope{Public: true}
	_ = s.Write(r)
	got, _ = viewB.Get(r.ID)
	if got == nil {
		t.Error("agent_b should see public record")
	}
}

// TestRetentionTTL tests that TTL records expire.
func TestRetentionTTL(t *testing.T) {
	s := openTestStore(t)
	reviewer := NewReviewerView(s)

	r := NewRecord(KindEpisode, SensLow, Provenance{
		WriterAgentID: "agent_a", WriterACI: "test@v0.1", Source: "user_input",
	})
	r.Episode = &Episode{Summary: "transient event"}
	r.Retention = Retention{Mode: "ttl", TTLSeconds: 0} // already expired
	if err := reviewer.Propose(r); err != nil {
		t.Fatal(err)
	}
	// Force expiry by setting ExpiresAt to past
	r.Retention.ExpiresAt = "2020-01-01T00:00:00Z"
	_ = s.Write(r)

	view := NewView(s, "agent_a", []string{})
	got, _ := view.Get(r.ID)
	if got != nil {
		t.Error("expired record should not be visible")
	}
}

// TestRevokeDeletesContent: revocation removes content and embeddings for
// real, and cannot be undone.
func TestRevokeDeletesContent(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(filepath.Join(dir, "m.db"), "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reviewer := NewReviewerView(s)

	r := NewRecord(KindBelief, SensLow, Provenance{WriterAgentID: "user", Source: "user_input"})
	r.Belief = &Belief{Claim: "lives in Paris ZXQV", Confidence: 1}
	_ = reviewer.Propose(r)
	_ = reviewer.Approve(r.ID)
	_ = s.WriteEmbedding(r.ID, []float32{0.1, 0.2}, "test")

	res, err := reviewer.Revoke(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.DeletedEmbeddings != 1 {
		t.Errorf("want 1 embedding deleted, got %d", res.DeletedEmbeddings)
	}
	if emb, _, _ := s.GetEmbedding(r.ID); emb != nil {
		t.Error("embedding should be deleted")
	}
	got, _ := s.Get(r.ID)
	if got.Status != StatusRevoked || got.Belief != nil {
		t.Errorf("record should be a content-free tombstone, got %+v", got)
	}
	if err := s.UnRevoke(r.ID); err == nil {
		t.Error("un-revoke must fail: content is gone")
	}
	// A second revoke reports no embeddings (none existed).
	res, _ = reviewer.Revoke(r.ID)
	if res.DeletedEmbeddings != 0 {
		t.Errorf("want 0 embeddings deleted on repeat, got %d", res.DeletedEmbeddings)
	}
}

// TestEncryptedAtRest: record content is not readable in the database file.
func TestEncryptedAtRest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.db")
	s, _ := OpenStore(path, "pw")
	r := NewRecord(KindPreference, SensLow, Provenance{WriterAgentID: "user", Source: "user_input"})
	r.Preference = &Preference{Key: "color", Value: "MAGENTAUNIQUE", Source: "user_stated", Confidence: 1}
	if err := s.Write(r); err != nil {
		t.Fatal(err)
	}
	s.Close()
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "MAGENTAUNIQUE") {
		t.Fatal("plaintext record content found in the database file")
	}
	if _, err := OpenStore(path, "wrong"); err == nil {
		s2, _ := OpenStore(path, "wrong")
		if _, err := s2.Get(r.ID); err == nil {
			t.Fatal("wrong passphrase must not decrypt")
		}
	}
}

// TestEvidenceCascade: revoking evidence revokes beliefs that cite it.
func TestEvidenceCascade(t *testing.T) {
	s := openTestStore(t)
	ev := NewRecord(KindEpisode, SensLow, Provenance{WriterAgentID: "user"})
	ev.Status = StatusActive
	ev.Episode = &Episode{Summary: "said they moved"}
	_ = s.Write(ev)
	b := NewRecord(KindBelief, SensLow, Provenance{WriterAgentID: "user"})
	b.Status = StatusActive
	b.Belief = &Belief{Claim: "lives in Rome", Evidence: []string{ev.ID}}
	_ = s.Write(b)
	if _, err := s.Revoke(ev.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(b.ID)
	if got.Status != StatusRevoked || got.Belief != nil {
		t.Errorf("belief citing revoked evidence should be revoked, got %+v", got)
	}
}

// TestSensitivityAndPurge covers the sensitive scope and TTL purge.
func TestTTLPurge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.db")
	s, _ := OpenStore(path, "pw")
	r := NewRecord(KindEpisode, SensLow, Provenance{WriterAgentID: "user"})
	r.Status = StatusActive
	r.Episode = &Episode{Summary: "old event"}
	r.Retention = Retention{Mode: "ttl", ExpiresAt: "2020-01-01T00:00:00Z"}
	_ = s.Write(r)
	s.Close()
	s, _ = OpenStore(path, "pw")
	defer s.Close()
	got, _ := s.Get(r.ID)
	if got.Status != StatusRevoked || got.Episode != nil {
		t.Errorf("expired record should be purged on open, got %+v", got)
	}
}

// TestSupersede tests that an old belief can be superseded by a new one.
func TestSupersede(t *testing.T) {
	s := openTestStore(t)
	reviewer := NewReviewerView(s)

	old := NewRecord(KindBelief, SensLow, Provenance{
		WriterAgentID: "agent_a", WriterACI: "test@v0.1", Source: "agent_inferred",
	})
	old.Belief = &Belief{Claim: "User works at Acme", Confidence: 0.7}
	_ = reviewer.Propose(old)
	_ = reviewer.Approve(old.ID)

	new := NewRecord(KindBelief, SensLow, Provenance{
		WriterAgentID: "agent_a", WriterACI: "test@v0.1", Source: "agent_inferred",
	})
	new.Belief = &Belief{Claim: "User works at Globex", Confidence: 0.95}
	_ = reviewer.Propose(new)
	_ = reviewer.Approve(new.ID)

	if err := s.Supersede(old.ID, new.ID); err != nil {
		t.Fatal(err)
	}

	oldAfter, _ := s.Get(old.ID)
	if oldAfter.Status != StatusSuperseded {
		t.Errorf("expected superseded, got %s", oldAfter.Status)
	}
	if oldAfter.Belief.SupersededBy != new.ID {
		t.Error("old belief should point to superseder")
	}
}

func prefRecord(key string) *Record {
	r := NewRecord(KindPreference, SensLow, Provenance{Source: "agent_inferred"})
	r.Preference = &Preference{Key: key, Value: "v", Source: "inferred", Confidence: 0.5}
	return r
}

func TestPendingTTLAndApprovalClearsIt(t *testing.T) {
	s := openTestStore(t)
	agent := NewViewOpts(s, "agent_a", nil, Options{Limits: Limits{PendingTTL: time.Hour}})
	r := prefRecord("k")
	if err := agent.Propose(r); err != nil {
		t.Fatal(err)
	}
	if r.Retention.Mode != "ttl" || !r.Retention.PendingTTL {
		t.Fatalf("pending record should carry a runtime TTL: %+v", r.Retention)
	}
	if err := NewReviewerView(s).Approve(r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(r.ID)
	if got.Retention.Mode != "until_revoked" || got.Retention.ExpiresAt != "" {
		t.Fatalf("approval should clear the pending TTL: %+v", got.Retention)
	}

	// An unapproved proposal past its TTL is purged, not merely hidden.
	old := prefRecord("old")
	if err := agent.Propose(old); err != nil {
		t.Fatal(err)
	}
	stale, _ := s.Get(old.ID)
	stale.Retention.ExpiresAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if err := s.Write(stale); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PurgeExpired(); err != nil || n != 1 {
		t.Fatalf("PurgeExpired = %d, %v; want 1", n, err)
	}
	if g, _ := s.Get(old.ID); g.Preference != nil {
		t.Fatal("expired pending content should be deleted")
	}
}

func TestPendingCapPerWriter(t *testing.T) {
	s := openTestStore(t)
	lim := Limits{MaxPendingPerWriter: 2}
	a := NewViewOpts(s, "agent_a", nil, Options{Limits: lim})
	b := NewViewOpts(s, "agent_b", nil, Options{Limits: lim})
	for i := 0; i < 2; i++ {
		if err := a.Propose(prefRecord("k")); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Propose(prefRecord("k")); err == nil || !strings.Contains(err.Error(), "pending limit") {
		t.Fatalf("third proposal should hit the cap, got %v", err)
	}
	if err := b.Propose(prefRecord("k")); err != nil {
		t.Fatalf("cap is per writer: %v", err)
	}
	an, _ := a.PendingCount()
	bn, _ := b.PendingCount()
	if an != 2 || bn != 1 {
		t.Fatalf("pending counts: a=%d b=%d", an, bn)
	}
	rev := NewReviewerView(s)
	for i := 0; i < 5; i++ {
		if err := rev.Propose(prefRecord("k")); err != nil {
			t.Fatalf("reviewer is exempt from the cap: %v", err)
		}
	}
	// Approving frees room.
	recs, _ := rev.List(KindPreference, 100)
	for _, r := range recs {
		if r.Provenance.WriterAgentID == "agent_a" {
			rev.Approve(r.ID)
			break
		}
	}
	if err := a.Propose(prefRecord("k")); err != nil {
		t.Fatalf("approval should free a slot: %v", err)
	}
}

func TestReviewerSeesAgentPending(t *testing.T) {
	s := openTestStore(t)
	a := NewViewOpts(s, "agent_a", nil, Options{})
	other := NewViewOpts(s, "agent_b", nil, Options{})
	r := prefRecord("k")
	a.Propose(r)
	if recs, _ := NewReviewerView(s).List(KindPreference, 10); len(recs) != 1 {
		t.Fatalf("reviewer should see agent proposals, got %d", len(recs))
	}
	if recs, _ := other.List(KindPreference, 10); len(recs) != 0 {
		t.Fatal("another agent must not see pending records")
	}
}

func TestWriteGateStagesOutsideRecordStore(t *testing.T) {
	s := openTestStore(t)
	agent := NewViewOpts(s, "agent_a", nil, Options{WriteGate: true})
	rev := NewReviewerView(s)

	r := prefRecord("secret-thing")
	if err := agent.Propose(r); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(r.ID); got != nil {
		t.Fatal("gated proposal must not be in the record store")
	}
	if all, _ := s.Query(QueryFilter{}); len(all) != 0 {
		t.Fatal("record store should be empty")
	}
	if recs, _ := agent.List("", 10); len(recs) != 0 {
		t.Fatal("writer cannot read back a gated proposal")
	}
	if got, _ := agent.Get(r.ID); got != nil {
		t.Fatal("writer cannot Get a gated proposal")
	}
	if hits, _ := agent.Search("secret-thing", 10); len(hits) != 0 {
		t.Fatal("gated proposal must not be searchable")
	}
	if n, _ := agent.PendingCount(); n != 1 {
		t.Fatalf("count still visible: %d", n)
	}
	if recs, _ := rev.List("", 10); len(recs) != 1 {
		t.Fatalf("reviewer sees the staged proposal, got %d", len(recs))
	}

	// Approve promotes it into the record store as active.
	if err := rev.Approve(r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(r.ID)
	if got == nil || got.Status != StatusActive || got.Retention.Mode != "until_revoked" {
		t.Fatalf("promoted record: %+v", got)
	}
	if p, _ := s.GetProposal(r.ID); p != nil {
		t.Fatal("staging row should be gone after promotion")
	}

	// Reject deletes the staged proposal outright.
	r2 := prefRecord("nope")
	agent.Propose(r2)
	if err := rev.Reject(r2.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.GetProposal(r2.ID); p != nil {
		t.Fatal("rejected proposal should be deleted")
	}
	if n, _ := s.PendingCount("agent_a"); n != 0 {
		t.Fatalf("pending after reject = %d", n)
	}
}

func TestWriteGateProposalsExpire(t *testing.T) {
	s := openTestStore(t)
	agent := NewViewOpts(s, "agent_a", nil, Options{WriteGate: true, Limits: Limits{PendingTTL: time.Hour}})
	r := prefRecord("k")
	agent.Propose(r)
	staged, _ := s.GetProposal(r.ID)
	staged.Retention.ExpiresAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	s.Stage(staged)
	if n, _ := s.PurgeExpired(); n != 1 {
		t.Fatalf("purged %d staged proposals, want 1", n)
	}
	if p, _ := s.GetProposal(r.ID); p != nil {
		t.Fatal("expired staged proposal should be deleted")
	}
}

// An agent must not be able to opt out of the pending expiry by supplying its
// own long TTL or a pre-set ExpiresAt.
func TestPendingExpiryCannotBeEvaded(t *testing.T) {
	s := openTestStore(t)
	agent := NewViewOpts(s, "agent_a", nil, Options{Limits: Limits{PendingTTL: time.Hour}})

	r := prefRecord("k")
	r.Retention = Retention{Mode: "ttl", TTLSeconds: 315360000, // ten years
		ExpiresAt: time.Now().UTC().AddDate(10, 0, 0).Format(time.RFC3339)}
	if err := agent.Propose(r); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(r.ID)
	exp, err := time.Parse(time.RFC3339, got.Retention.ExpiresAt)
	if err != nil || time.Until(exp) > time.Hour+time.Minute {
		t.Fatalf("pending expiry must be clamped to the pending TTL, got %s", got.Retention.ExpiresAt)
	}

	// Approval hands the writer's own retention back (a TTL restarts at approval).
	if err := NewReviewerView(s).Approve(r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(r.ID)
	if got.Retention.Mode != "ttl" || got.Retention.TTLSeconds != 315360000 || got.Retention.PendingTTL {
		t.Fatalf("approval should restore the requested TTL: %+v", got.Retention)
	}
	if exp, _ := time.Parse(time.RFC3339, got.Retention.ExpiresAt); time.Until(exp) < 24*time.Hour {
		t.Fatalf("restored TTL should restart from approval: %s", got.Retention.ExpiresAt)
	}

	// A shorter TTL than the pending TTL is honoured as is.
	short := prefRecord("short")
	short.Retention = Retention{Mode: "ttl", TTLSeconds: 60}
	agent.Propose(short)
	if got, _ := s.Get(short.ID); got.Retention.TTLSeconds != 60 || got.Retention.PendingTTL {
		t.Fatalf("shorter agent TTL should stand: %+v", got.Retention)
	}

	// The same clamp applies to write-gated proposals.
	gated := NewViewOpts(s, "agent_b", nil, Options{WriteGate: true, Limits: Limits{PendingTTL: time.Hour}})
	g := prefRecord("g")
	g.Retention = Retention{Mode: "ttl", TTLSeconds: 315360000}
	gated.Propose(g)
	st, _ := s.GetProposal(g.ID)
	if exp, _ := time.Parse(time.RFC3339, st.Retention.ExpiresAt); time.Until(exp) > time.Hour+time.Minute {
		t.Fatalf("staged proposal expiry not clamped: %s", st.Retention.ExpiresAt)
	}
}

// Reviewer privilege belongs to the view: an agent handed a "reviewer" scope
// string still cannot read other writers' pending records.
func TestReviewerScopeStringGrantsNothing(t *testing.T) {
	s := openTestStore(t)
	NewViewOpts(s, "agent_a", nil, Options{}).Propose(prefRecord("k"))
	spoof := NewViewOpts(s, "agent_b", []string{"reviewer"}, Options{})
	if recs, _ := spoof.List(KindPreference, 10); len(recs) != 0 {
		t.Fatal("a reviewer scope string must not reveal other writers' pending records")
	}
	if recs, _ := NewReviewerView(s).List(KindPreference, 10); len(recs) != 1 {
		t.Fatal("the real reviewer view must still see it")
	}
}

// A closed store must surface as an error, not as "0 pending".
func TestPendingCountReportsErrors(t *testing.T) {
	s := openTestStore(t)
	v := NewViewOpts(s, "agent_a", nil, Options{})
	s.Close()
	if _, err := v.PendingCount(); err == nil {
		t.Fatal("expected an error from a closed store")
	}
}
