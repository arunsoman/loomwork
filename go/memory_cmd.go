package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"loomwork.dev/loomwork/internal/memory"
)

// cmdMemory is the typed-memory CLI.
//
// Usage:
//
//	loomwork memory list [--kind preference|episode|artifact|belief|failure] [--status pending|active|revoked]
//	loomwork memory get <id>
//	loomwork memory propose --kind belief --claim "..." [--evidence id1,id2]
//	loomwork memory approve <id>
//	loomwork memory reject <id>
//	loomwork memory revoke <id>     # cascades to derivatives
//	loomwork memory graph <id>      # show derivative graph
//	loomwork memory stats
func cmdMemory(args []string) {
	if len(args) < 1 {
		fmt.Print(`loomwork memory — typed memory management

Usage:
  loomwork memory list [--kind K] [--status S]   List records (filtered)
  loomwork memory get <id>                        Show one record
  loomwork memory propose --kind K --json '{...}' [--allow-agent a,b] [--public] [--ttl secs] [--as-agent name]
                                                  Propose a new record (pending)
  loomwork memory approve <id>                    Approve a pending record
  loomwork memory reject <id>                     Reject a pending record
  loomwork memory revoke <id>                     Revoke + cascade to derivatives
  loomwork memory graph <id>                      Show derivative graph
  loomwork memory stats                           Show counts by kind/status

Record kinds: preference, episode, artifact, belief, failure
Record statuses: pending, active, superseded, revoked
`)
		return
	}
	sub := args[0]
	rest := args[1:]

	store, err := memory.OpenDefaultStore(homeDir())
	if err != nil {
		fail(err)
	}
	defer store.Close()
	view := memory.NewReviewerView(store)

	switch sub {
	case "list":
		fs := flag.NewFlagSet("list", flag.ExitOnError)
		kind := fs.String("kind", "", "filter by kind")
		status := fs.String("status", "", "filter by status")
		limit := fs.Int("limit", 20, "max records")
		parseArgs(fs, rest)
		records, err := view.List(memory.Kind(*kind), 1<<30)
		if err != nil {
			fail(err)
		}
		staged := map[string]bool{}
		if ids, err := store.StagedIDs(); err == nil {
			for _, id := range ids {
				staged[id] = true
			}
		}
		shown := 0
		for _, r := range records {
			if *status != "" && string(r.Status) != *status {
				continue
			}
			if shown >= *limit {
				break
			}
			shown++
			preview := recordPreview(r)
			label := string(r.Status)
			if staged[r.ID] {
				label += " (staged)"
			}
			fmt.Printf("  %s  [%s]  %s  %s\n", r.ID, r.Kind, label, preview)
		}
	case "get":
		if len(rest) < 1 {
			fail(fmt.Errorf("usage: loomwork memory get <id>"))
		}
		r, err := view.Get(rest[0])
		if err != nil {
			fail(err)
		}
		if r == nil {
			fail(fmt.Errorf("not found (or not visible)"))
		}
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(b))
	case "propose":
		fs := flag.NewFlagSet("propose", flag.ExitOnError)
		kind := fs.String("kind", "", "record kind (required)")
		payloadJSON := fs.String("json", "", "JSON payload for the typed record (required)")
		evidence := fs.String("evidence", "", "comma-separated evidence record IDs (for beliefs)")
		parent := fs.String("parent", "", "parent record ID (if derived)")
		sensitivity := fs.String("sensitivity", "low", "low|medium|high")
		allowAgents := fs.String("allow-agent", "", "comma-separated agent names allowed to read this record (agent name = metadata.name)")
		scopes := fs.String("allow-scope", "", "comma-separated scopes allowed to read this record")
		public := fs.Bool("public", false, "any agent may read this record")
		ttl := fs.Int64("ttl", 0, "delete the record after this many seconds (default: keep until revoked)")
		asAgent := fs.String("as-agent", "", "propose as this agent (subject to the pending cap, pending TTL and write-gate) instead of as the reviewer")
		parseArgs(fs, rest)
		if *kind == "" || *payloadJSON == "" {
			fail(fmt.Errorf("--kind and --json are required"))
		}
		r, err := buildRecord(*kind, *payloadJSON, *sensitivity, *parent, *evidence)
		if err != nil {
			fail(err)
		}
		if *allowAgents != "" {
			r.Consent.AllowedAgents = append(r.Consent.AllowedAgents, strings.Split(*allowAgents, ",")...)
		}
		if *scopes != "" {
			r.Consent.AllowedScopes = strings.Split(*scopes, ",")
		}
		r.Consent.Public = *public
		if *ttl > 0 {
			r.Retention = memory.Retention{Mode: "ttl", TTLSeconds: *ttl}
		}
		writer := view
		if *asAgent != "" {
			writer = memory.NewView(store, *asAgent, nil)
			// The writer can always read its own record; keep any --allow-agent recipients.
			if !containsString(r.Consent.AllowedAgents, *asAgent) {
				r.Consent.AllowedAgents = append(r.Consent.AllowedAgents, *asAgent)
			}
		}
		if err := writer.Propose(r); err != nil {
			fail(err)
		}
		where := "status: pending"
		if *asAgent != "" && memory.WriteGateFromEnv() {
			where = "staged in the proposals table (write-gate), outside the record store"
		}
		fmt.Printf("✓ Proposed %s (id: %s, %s)\n", *kind, r.ID, where)
		fmt.Printf("  Approve with: loomwork memory approve %s\n", r.ID)
	case "approve":
		if len(rest) < 1 {
			fail(fmt.Errorf("usage: loomwork memory approve <id>"))
		}
		if err := view.Approve(rest[0]); err != nil {
			fail(err)
		}
		fmt.Printf("✓ Approved %s (now active)\n", rest[0])
	case "reject":
		if len(rest) < 1 {
			fail(fmt.Errorf("usage: loomwork memory reject <id>"))
		}
		if err := view.Reject(rest[0]); err != nil {
			fail(err)
		}
		fmt.Printf("✓ Rejected %s (content deleted, never durable)\n", rest[0])
	case "revoke":
		if len(rest) < 1 {
			fail(fmt.Errorf("usage: loomwork memory revoke <id>"))
		}
		result, err := view.Revoke(rest[0])
		if err != nil {
			fail(err)
		}
		fmt.Printf("✓ Revoked %s (content deleted; this cannot be undone)\n", rest[0])
		fmt.Printf("  Cascade: %d records revoked, %d embeddings deleted, depth=%d\n",
			len(result.RevokedRecords), result.DeletedEmbeddings, result.Depth)
		if len(result.RevokedRecords) > 1 {
			fmt.Printf("  Revoked records:\n")
			for _, id := range result.RevokedRecords {
				fmt.Printf("    - %s\n", id)
			}
		}
	case "graph":
		if len(rest) < 1 {
			fail(fmt.Errorf("usage: loomwork memory graph <id>"))
		}
		graph, err := buildDerivativeGraph(store, rest[0], 0)
		if err != nil {
			fail(err)
		}
		printGraph(graph, "")
	case "stats":
		stats, err := store.ComputeStats()
		if err != nil {
			fail(err)
		}
		fmt.Printf("Total records: %d\n", stats.Total)
		fmt.Printf("Embeddings:    %d\n", stats.Embeddings)
		fmt.Printf("Staged:        %d  (write-gate proposals awaiting review; see `loomwork memory list --status pending`)\n", stats.Staged)
		fmt.Println("\nBy kind:")
		for k, n := range stats.ByKind {
			fmt.Printf("  %s: %d\n", k, n)
		}
		fmt.Println("\nBy status:")
		for s, n := range stats.ByStatus {
			fmt.Printf("  %s: %d\n", s, n)
		}
	default:
		fail(fmt.Errorf("unknown subcommand: %s", sub))
	}
}

func recordPreview(r *memory.Record) string {
	switch {
	case r.Preference != nil:
		return fmt.Sprintf("%s=%s", r.Preference.Key, r.Preference.Value)
	case r.Episode != nil:
		return truncateStr(r.Episode.Summary, 60)
	case r.Artifact != nil:
		return r.Artifact.Name
	case r.Belief != nil:
		return truncateStr(r.Belief.Claim, 60)
	case r.Failure != nil:
		return truncateStr(r.Failure.Pattern, 60)
	}
	return ""
}

func buildRecord(kind, payloadJSON, sensitivity, parent, evidence string) (*memory.Record, error) {
	sens := memory.Sensitivity(sensitivity)
	prov := memory.Provenance{
		WriterAgentID: "user",
		WriterACI:     "loomwork-cli",
		Source:        "user_input",
		ParentID:      parent,
	}
	r := memory.NewRecord(memory.Kind(kind), sens, prov)
	switch memory.Kind(kind) {
	case memory.KindPreference:
		var p memory.Preference
		if err := json.Unmarshal([]byte(payloadJSON), &p); err != nil {
			return nil, err
		}
		r.Preference = &p
	case memory.KindEpisode:
		var e memory.Episode
		if err := json.Unmarshal([]byte(payloadJSON), &e); err != nil {
			return nil, err
		}
		r.Episode = &e
	case memory.KindArtifact:
		var a memory.Artifact
		if err := json.Unmarshal([]byte(payloadJSON), &a); err != nil {
			return nil, err
		}
		r.Artifact = &a
	case memory.KindBelief:
		var b memory.Belief
		if err := json.Unmarshal([]byte(payloadJSON), &b); err != nil {
			return nil, err
		}
		if evidence != "" {
			b.Evidence = strings.Split(evidence, ",")
		}
		r.Belief = &b
	case memory.KindFailure:
		var f memory.Failure
		if err := json.Unmarshal([]byte(payloadJSON), &f); err != nil {
			return nil, err
		}
		r.Failure = &f
	default:
		return nil, fmt.Errorf("unknown kind: %s", kind)
	}
	return r, nil
}

type graphNode struct {
	Record   *memory.Record
	Children []*graphNode
}

func buildDerivativeGraph(store *memory.Store, id string, depth int) (*graphNode, error) {
	if depth > 20 {
		return nil, nil // cycle guard
	}
	r, err := store.Get(id)
	if err != nil || r == nil {
		return nil, err
	}
	node := &graphNode{Record: r}
	children, err := store.GetChildren(id)
	if err != nil {
		return nil, err
	}
	for _, c := range children {
		child, err := buildDerivativeGraph(store, c, depth+1)
		if err != nil {
			continue
		}
		if child != nil {
			node.Children = append(node.Children, child)
		}
	}
	return node, nil
}

func printGraph(node *graphNode, indent string) {
	if node == nil || node.Record == nil {
		return
	}
	status := node.Record.Status
	if status == memory.StatusRevoked {
		status = "REVOKED"
	}
	fmt.Printf("%s├─ %s [%s, %s] %s\n", indent, node.Record.ID, node.Record.Kind, status, recordPreview(node.Record))
	for _, c := range node.Children {
		printGraph(c, indent+"  ")
	}
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
