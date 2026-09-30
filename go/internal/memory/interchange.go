package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Markdown interchange: import reads a markdown file into pending records;
// export writes active records out as markdown. One direction at a time, no
// sync, no round-trip writing into the source file.
//
// Export format: one block per record.
//
//	---
//	kind: belief
//	id: mem_...
//	created: 2026-01-02T03:04:05Z
//	sensitivity: low
//	provenance.writer: bot
//	provenance.source: agent_inferred
//	consent: public            (or "private", or allowedAgents/scopes below)
//	consent.allowedAgents: ["a","b"]
//	consent.allowedScopes: ["research"]
//	payload: {"claim":"..."}   (the typed payload, one line of JSON)
//	---
//	the record content as markdown
//
// Import of this format restores kind, sensitivity, provenance and consent and
// the typed payload; new IDs are generated. It does not preserve IDs,
// retention, derivative/evidence links (they name old IDs), or status.

const (
	frontMatterDelim = "---"
	// MaxImportBytes bounds a markdown file read by ImportMarkdown callers.
	MaxImportBytes = 4 << 20
)

// PlainImportOptions apply to plain conventional markdown only; export-format
// records take their consent and sensitivity from their front-matter.
type PlainImportOptions struct {
	Kind          Kind // default belief
	Sensitivity   Sensitivity
	AllowedAgents []string
	Public        bool
	SourceURI     string
}

// IsExportFormat reports whether text is in the Loomwork export format.
func IsExportFormat(text string) bool {
	lines := strings.Split(normalizeNewlines(text), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		return l == frontMatterDelim && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "kind:")
	}
	return false
}

// ---------------------------------------------------------------- export

// ExportMarkdown renders active, unexpired records as markdown blocks, oldest
// first. The store's owner sees every active record regardless of consent
// scope; consent is rendered into the front-matter, not applied as a filter.
func (s *Store) ExportMarkdown() (string, int, error) {
	var recs []*Record
	for offset := 0; ; offset += scanPage {
		page, err := s.Query(QueryFilter{Status: StatusActive, Limit: scanPage, Offset: offset})
		if err != nil {
			return "", 0, err
		}
		for _, r := range page {
			if !r.IsExpired() {
				recs = append(recs, r)
			}
		}
		if len(page) < scanPage {
			break
		}
	}
	// Query is newest-first; emit oldest-first for a stable, readable file.
	for i, j := 0, len(recs)-1; i < j; i, j = i+1, j-1 {
		recs[i], recs[j] = recs[j], recs[i]
	}
	var b strings.Builder
	for _, r := range recs {
		block, err := renderBlock(r)
		if err != nil {
			return "", 0, err
		}
		b.WriteString(block)
	}
	return b.String(), len(recs), nil
}

var safeScalar = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:@/+-]*$`)

// scalar renders a front-matter value: bare when unambiguous, JSON otherwise.
func scalar(v string) string {
	if safeScalar.MatchString(v) {
		return v
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func jsonList(v []string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func renderBlock(r *Record) (string, error) {
	payload, err := payloadJSON(r)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nkind: %s\nid: %s\ncreated: %s\nsensitivity: %s\n", frontMatterDelim, r.Kind, scalar(r.ID), scalar(r.CreatedAt), r.Sensitivity)
	fmt.Fprintf(&b, "provenance.writer: %s\nprovenance.source: %s\n", scalar(r.Provenance.WriterAgentID), scalar(r.Provenance.Source))
	if r.Provenance.WriterACI != "" {
		fmt.Fprintf(&b, "provenance.aci: %s\n", scalar(r.Provenance.WriterACI))
	}
	if r.Provenance.SourceURI != "" {
		fmt.Fprintf(&b, "provenance.sourceUri: %s\n", scalar(r.Provenance.SourceURI))
	}
	switch {
	case r.Consent.Public:
		b.WriteString("consent: public\n")
	case len(r.Consent.AllowedAgents) == 0 && len(r.Consent.AllowedScopes) == 0:
		b.WriteString("consent: private\n")
	default:
		b.WriteString("consent: restricted\n")
	}
	if len(r.Consent.AllowedAgents) > 0 {
		fmt.Fprintf(&b, "consent.allowedAgents: %s\n", jsonList(r.Consent.AllowedAgents))
	}
	if len(r.Consent.AllowedScopes) > 0 {
		fmt.Fprintf(&b, "consent.allowedScopes: %s\n", jsonList(r.Consent.AllowedScopes))
	}
	fmt.Fprintf(&b, "payload: %s\n%s\n", payload, frontMatterDelim)
	b.WriteString(escapeBody(strings.TrimRight(renderContent(r), "\n")))
	b.WriteString("\n\n")
	return b.String(), nil
}

// escapeBody keeps a content line that is exactly `---` from being read as a
// block delimiter. The body is informational; the payload is authoritative.
func escapeBody(s string) string {
	lines := strings.Split(normalizeNewlines(s), "\n")
	for i, l := range lines {
		if l == frontMatterDelim {
			lines[i] = "\\" + l
		}
	}
	return strings.Join(lines, "\n")
}

// payloadJSON is the record's typed payload on one line.
func payloadJSON(r *Record) (string, error) {
	var v interface{}
	switch {
	case r.Preference != nil:
		v = r.Preference
	case r.Episode != nil:
		v = r.Episode
	case r.Artifact != nil:
		v = r.Artifact
	case r.Belief != nil:
		v = r.Belief
	case r.Failure != nil:
		v = r.Failure
	default:
		return "", fmt.Errorf("record %s has no payload", r.ID)
	}
	b, err := json.Marshal(v)
	return string(b), err
}

// renderContent is the record's content as human-readable markdown.
func renderContent(r *Record) string {
	switch {
	case r.Belief != nil:
		return r.Belief.Claim
	case r.Preference != nil:
		return r.Preference.Key + ": " + r.Preference.Value
	case r.Episode != nil:
		return r.Episode.Summary
	case r.Artifact != nil:
		return r.Artifact.Name
	case r.Failure != nil:
		return fmt.Sprintf("%s\n\nWhat failed: %s\nWhy: %s\nAvoid: %s", r.Failure.Pattern, r.Failure.WhatFailed, r.Failure.Why, r.Failure.Avoidance)
	}
	return ""
}

// ---------------------------------------------------------------- import

// ImportMarkdown parses text and proposes every record through the reviewer
// view, so all of them are created pending; nothing is activated. It returns
// the new record IDs. It never deduplicates: importing twice creates twice.
func ImportMarkdown(v *View, text string, plain PlainImportOptions) ([]string, error) {
	if !v.canApprove {
		return nil, fmt.Errorf("import requires a reviewer view")
	}
	var recs []*Record
	var err error
	if IsExportFormat(text) {
		recs, err = parseExportFormat(text)
	} else {
		recs, err = parsePlain(text, plain)
	}
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("nothing to import: no `## ` sections found (plain markdown is split on level-2 headings)")
	}
	// Propose only after everything parsed, so a bad file imports nothing.
	ids := make([]string, 0, len(recs))
	for _, r := range recs {
		if err := v.Propose(r); err != nil {
			return ids, err
		}
		ids = append(ids, r.ID)
	}
	return ids, nil
}

func normalizeNewlines(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// parsePlain splits conventional markdown on `## ` headings (outside code
// fences); each section becomes one record. Text before the first heading is
// not imported.
func parsePlain(text string, o PlainImportOptions) ([]*Record, error) {
	kind := o.Kind
	if kind == "" {
		kind = KindBelief
	}
	sens := o.Sensitivity
	if sens == "" {
		sens = SensLow
	}
	type section struct{ heading, body string }
	var secs []section
	var cur *section
	var body []string
	flush := func() {
		if cur != nil {
			cur.body = strings.TrimSpace(strings.Join(body, "\n"))
			secs = append(secs, *cur)
		}
		body = nil
	}
	fence := false
	for _, line := range strings.Split(normalizeNewlines(text), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") || strings.HasPrefix(strings.TrimSpace(line), "~~~") {
			fence = !fence
		}
		if !fence && strings.HasPrefix(line, "## ") {
			flush()
			cur = &section{heading: strings.TrimSpace(strings.TrimLeft(line, "# "))}
			continue
		}
		if cur != nil {
			body = append(body, line)
		}
	}
	flush()

	var out []*Record
	for _, s := range secs {
		if s.heading == "" && s.body == "" {
			continue
		}
		prov := Provenance{WriterAgentID: "user", WriterACI: "loomwork-cli", Source: "imported", SourceURI: o.SourceURI}
		r := NewRecord(kind, sens, prov)
		content := strings.TrimSpace(s.heading + "\n\n" + s.body)
		switch kind {
		case KindBelief:
			r.Belief = &Belief{Claim: content, Confidence: 0.5}
		case KindPreference:
			r.Preference = &Preference{Key: s.heading, Value: s.body, Source: "imported", Confidence: 0.5}
		case KindEpisode:
			r.Episode = &Episode{Timestamp: time.Now().UTC().Format(time.RFC3339), Summary: content}
		case KindFailure:
			r.Failure = &Failure{Pattern: s.heading, WhatFailed: s.body}
		case KindArtifact:
			sum := sha256.Sum256([]byte(content))
			r.Artifact = &Artifact{Name: s.heading, Mime: "text/markdown", Digest: "sha256:" + hex.EncodeToString(sum[:]), CreatedBy: "loomwork-import"}
		default:
			return nil, fmt.Errorf("unknown kind: %s", kind)
		}
		r.Consent = ConsentScope{Public: o.Public, AllowedAgents: append([]string{"user"}, o.AllowedAgents...)}
		out = append(out, r)
	}
	return out, nil
}

// parseExportFormat reads blocks written by ExportMarkdown.
func parseExportFormat(text string) ([]*Record, error) {
	lines := strings.Split(normalizeNewlines(text), "\n")
	var out []*Record
	for i := 0; i < len(lines); {
		if strings.TrimSpace(lines[i]) == "" {
			i++
			continue
		}
		if lines[i] != frontMatterDelim || i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "kind:") {
			return nil, fmt.Errorf("line %d: expected a record block starting with `---` and `kind:`", i+1)
		}
		start := i + 1
		i++
		fm := map[string]string{}
		for ; i < len(lines) && lines[i] != frontMatterDelim; i++ {
			k, v, ok := strings.Cut(lines[i], ":")
			if !ok {
				return nil, fmt.Errorf("line %d: malformed front-matter %q", i+1, lines[i])
			}
			fm[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		if i >= len(lines) {
			return nil, fmt.Errorf("line %d: front-matter block is not closed", start)
		}
		i++ // closing ---
		// The body runs to the next block start; it is informational, the
		// payload in the front-matter is the source of truth.
		for i < len(lines) && !(lines[i] == frontMatterDelim && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "kind:")) {
			i++
		}
		r, err := recordFromFrontMatter(fm)
		if err != nil {
			return nil, fmt.Errorf("record at line %d: %w", start, err)
		}
		out = append(out, r)
	}
	return out, nil
}

func fmString(fm map[string]string, key string) (string, error) {
	v, ok := fm[key]
	if !ok {
		return "", nil
	}
	if strings.HasPrefix(v, `"`) {
		var s string
		if err := json.Unmarshal([]byte(v), &s); err != nil {
			return "", fmt.Errorf("%s: %w", key, err)
		}
		return s, nil
	}
	return v, nil
}

func fmList(fm map[string]string, key string) ([]string, error) {
	v, ok := fm[key]
	if !ok {
		return nil, nil
	}
	var l []string
	if err := json.Unmarshal([]byte(v), &l); err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return l, nil
}

func recordFromFrontMatter(fm map[string]string) (*Record, error) {
	var err error
	get := func(key string) string {
		var s string
		if err == nil {
			s, err = fmString(fm, key)
		}
		return s
	}
	kind := Kind(get("kind"))
	sens := Sensitivity(get("sensitivity"))
	if sens == "" {
		sens = SensLow
	}
	switch sens {
	case SensLow, SensMedium, SensHigh:
	default:
		return nil, fmt.Errorf("invalid sensitivity %q", sens)
	}
	prov := Provenance{
		WriterAgentID: get("provenance.writer"),
		WriterACI:     get("provenance.aci"),
		Source:        get("provenance.source"),
		SourceURI:     get("provenance.sourceUri"),
		WrittenAt:     get("created"),
	}
	if err != nil {
		return nil, err
	}
	if prov.WriterAgentID == "" {
		prov.WriterAgentID = "user"
	}
	if prov.Source == "" {
		prov.Source = "imported"
	}
	r := NewRecord(kind, sens, prov)
	agents, e1 := fmList(fm, "consent.allowedAgents")
	scopes, e2 := fmList(fm, "consent.allowedScopes")
	if e1 != nil {
		return nil, e1
	}
	if e2 != nil {
		return nil, e2
	}
	r.Consent = ConsentScope{Public: get("consent") == "public", AllowedAgents: agents, AllowedScopes: scopes}
	if err != nil {
		return nil, err
	}
	raw, ok := fm["payload"]
	if !ok {
		return nil, fmt.Errorf("missing payload")
	}
	unmarshal := func(dst interface{}) error { return json.Unmarshal([]byte(raw), dst) }
	switch kind {
	case KindPreference:
		r.Preference = &Preference{}
		err = unmarshal(r.Preference)
	case KindEpisode:
		r.Episode = &Episode{}
		if err = unmarshal(r.Episode); err == nil {
			r.Episode.Inputs, r.Episode.Outputs = nil, nil // named old IDs
		}
	case KindArtifact:
		r.Artifact = &Artifact{}
		err = unmarshal(r.Artifact)
	case KindBelief:
		r.Belief = &Belief{}
		if err = unmarshal(r.Belief); err == nil {
			r.Belief.Evidence, r.Belief.SupersededBy = nil, "" // named old IDs
		}
	case KindFailure:
		r.Failure = &Failure{}
		err = unmarshal(r.Failure)
	default:
		return nil, fmt.Errorf("unknown kind %q", kind)
	}
	if err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	return r, nil
}
