package memory

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func newStoreAt(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "i.db"), "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

const plainMD = "# Memory\n\nintro text is not a section\n\n## Coffee\nlikes flat white\n\n## Editor\nuses vim\n\n```\n## not a heading\n```\n\n## Timezone\nUTC+1\n"

func TestImportPlainMarkdownCreatesPendingRecords(t *testing.T) {
	s := newStoreAt(t)
	ids, err := ImportMarkdown(NewReviewerView(s), plainMD, PlainImportOptions{SourceURI: "file:///x/MEMORY.md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Fatalf("want 3 records, got %d", len(ids))
	}
	if active, _ := s.Query(QueryFilter{Status: StatusActive}); len(active) != 0 {
		t.Fatalf("import must not activate anything, got %d active", len(active))
	}
	for _, id := range ids {
		r, _ := s.Get(id)
		if r.Status != StatusPending || r.Kind != KindBelief || r.Sensitivity != SensLow ||
			r.Provenance.Source != "imported" || r.Consent.Public || len(r.Consent.AllowedAgents) != 1 || r.Consent.AllowedAgents[0] != "user" {
			t.Fatalf("bad imported record: %+v", r)
		}
	}
	// Importing again duplicates (no dedup, by design).
	if ids2, _ := ImportMarkdown(NewReviewerView(s), plainMD, PlainImportOptions{}); len(ids2) != 3 {
		t.Fatal("re-import should create 3 more records")
	}
	if pend, _ := s.Query(QueryFilter{Status: StatusPending}); len(pend) != 6 {
		t.Fatalf("expected 6 pending after two imports, got %d", len(pend))
	}
}

func TestImportPlainWithoutSectionsIsAnError(t *testing.T) {
	if _, err := ImportMarkdown(NewReviewerView(newStoreAt(t)), "# just a title\nno sections\n", PlainImportOptions{}); err == nil {
		t.Fatal("expected an error for a file with no ## sections")
	}
}

func TestImportRequiresReviewerView(t *testing.T) {
	s := newStoreAt(t)
	if _, err := ImportMarkdown(NewView(s, "bot", nil), plainMD, PlainImportOptions{}); err == nil {
		t.Fatal("agent views must not import")
	}
}

const exportFmt = "---\nkind: belief\nid: mem_old\ncreated: 2026-01-02T03:04:05Z\nsensitivity: medium\nprovenance.writer: bot\nprovenance.source: agent_inferred\nconsent: public\nconsent.allowedScopes: [\"research\"]\npayload: {\"claim\":\"Sky is blue\",\"evidence\":[\"mem_gone\"],\"confidence\":0.9}\n---\nSky is blue\n"

func TestImportExportFormatRestoresFields(t *testing.T) {
	s := newStoreAt(t)
	ids, err := ImportMarkdown(NewReviewerView(s), exportFmt, PlainImportOptions{})
	if err != nil || len(ids) != 1 {
		t.Fatalf("%v %v", ids, err)
	}
	r, _ := s.Get(ids[0])
	if ids[0] == "mem_old" || r.Status != StatusPending || r.Kind != KindBelief || !r.Consent.Public ||
		r.Provenance.WriterAgentID != "bot" || r.Provenance.Source != "agent_inferred" || r.Sensitivity != SensMedium ||
		r.Belief.Claim != "Sky is blue" || len(r.Belief.Evidence) != 0 || len(r.Consent.AllowedScopes) != 1 {
		t.Fatalf("bad restore: %+v %+v", r, r.Belief)
	}
}

var volatile = regexp.MustCompile(`(?m)^(id|created): .*\n`)

func TestExportRoundTrip(t *testing.T) {
	a := newStoreAt(t)
	rev := NewReviewerView(a)
	agent := NewViewOpts(a, "bot", nil, Options{Limits: Limits{PendingTTL: 3600e9}})

	pub := NewRecord(KindBelief, SensLow, Provenance{Source: "agent_inferred"})
	pub.Belief = &Belief{Claim: "Line one.\n\n---\nkind: fake\n", Confidence: 0.8}
	pub.Consent = ConsentScope{Public: true}
	pref := NewRecord(KindPreference, SensHigh, Provenance{Source: "user_input"})
	pref.Preference = &Preference{Key: "tone: \"dry\"", Value: "terse", Source: "user_stated", Confidence: 1}
	pref.Consent = ConsentScope{AllowedAgents: []string{"bot", "other"}, AllowedScopes: []string{"writing"}}
	pending := prefRecord("stays-pending")
	rejected := prefRecord("rejected")
	for _, r := range []*Record{pub, pref, pending, rejected} {
		if err := agent.Propose(r); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []*Record{pub, pref} {
		if err := rev.Approve(r.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := rev.Reject(rejected.ID); err != nil {
		t.Fatal(err)
	}

	out1, n, err := a.ExportMarkdown()
	if err != nil || n != 2 {
		t.Fatalf("want 2 active records exported, got %d (%v)", n, err)
	}
	if strings.Contains(out1, "stays-pending") || strings.Contains(out1, "rejected") {
		t.Fatal("export leaked a non-active record")
	}

	b := newStoreAt(t)
	ids, err := ImportMarkdown(NewReviewerView(b), out1, PlainImportOptions{})
	if err != nil || len(ids) != 2 {
		t.Fatalf("import: %v %v", ids, err)
	}
	for _, id := range ids {
		if err := NewReviewerView(b).Approve(id); err != nil {
			t.Fatal(err)
		}
	}
	out2, n2, err := b.ExportMarkdown()
	if err != nil || n2 != 2 {
		t.Fatalf("second export: %d %v", n2, err)
	}
	// Everything but the volatile id/created lines is identical: kind, content,
	// consent, writer, source, sensitivity, payload.
	if volatile.ReplaceAllString(out1, "") != volatile.ReplaceAllString(out2, "") {
		t.Fatalf("round trip differs:\n--- first\n%s\n--- second\n%s", out1, out2)
	}
}
