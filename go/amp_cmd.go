package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"loomwork.dev/loomwork/internal/aci"
	"loomwork.dev/loomwork/internal/amp"
	"loomwork.dev/loomwork/internal/llm"
	"loomwork.dev/loomwork/internal/runtime"
)

// cmdAmp hands work between agents over the Agent Mesh Protocol (stdio).
//
//	loomwork amp token --skill answer_question [--ttl 10m] [--out token.json]
//	loomwork amp serve <agent.aci|dir>
//	loomwork amp delegate --exec "loomwork amp serve b.aci" --from a.aci \
//	      --token token.json "summarize this"
//
// A caller proves who it is with signed provenance and is authorized by a
// capability token signed by a key the callee trusts.
func cmdAmp(args []string) {
	if len(args) < 1 {
		fmt.Print(`loomwork amp — delegate a task to another agent

  loomwork amp token --skill S [--ttl 10m] [--out f]   Issue a signed capability token
  loomwork amp serve <agent.aci|dir>                   Serve amp/delegate on stdin/stdout
  loomwork amp delegate --exec "CMD" --from <agent.aci> --token f "intent"
                                                       Send a task to a peer started by CMD

The callee must trust the caller's key (loomwork trust add).
`)
		return
	}
	switch args[0] {
	case "token":
		ampToken(args[1:])
	case "serve":
		ampServe(args[1:])
	case "delegate":
		ampDelegate(args[1:])
	default:
		fail(fmt.Errorf("unknown amp subcommand %q", args[0]))
	}
}

func ampToken(args []string) {
	fs := flag.NewFlagSet("amp token", flag.ExitOnError)
	skill := fs.String("skill", "answer_question", "skill the token authorizes")
	ttl := fs.Duration("ttl", 10*time.Minute, "how long the token is valid")
	out := fs.String("out", "", "write the token here (default: stdout)")
	parseArgs(fs, args)
	sk := loadOrCreateKey(defaultKeyPath())
	now := time.Now().UTC()
	tok := &amp.CapabilityToken{
		ID:        amp.NewID("cap"),
		Skill:     *skill,
		IssuedAt:  now.Format(time.RFC3339),
		ExpiresAt: now.Add(*ttl).Format(time.RFC3339),
	}
	if err := amp.SignCapability(tok, sk); err != nil {
		fail(err)
	}
	b, _ := json.MarshalIndent(tok, "", "  ")
	if *out == "" {
		fmt.Println(string(b))
		return
	}
	if err := os.WriteFile(*out, append(b, '\n'), 0o600); err != nil {
		fail(err)
	}
	fmt.Fprintf(os.Stderr, "✓ Token for %q valid until %s written to %s\n", *skill, tok.ExpiresAt, *out)
}

func ampServe(args []string) {
	fs := flag.NewFlagSet("amp serve", flag.ExitOnError)
	allowUnsigned := fs.Bool("allow-unsigned", false, "serve an ACI whose signature is missing or invalid (unsafe)")
	allowUntrusted := fs.Bool("allow-untrusted-signer", false, "serve an ACI signed by an untrusted key")
	pos := parseArgs(fs, args)
	if len(pos) != 1 {
		fail(fmt.Errorf("usage: loomwork amp serve <agent.aci | dir>"))
	}
	path := pos[0]

	var archive *aci.Archive
	if strings.HasSuffix(path, ".aci") {
		raw, err := os.ReadFile(path)
		if err != nil {
			fail(err)
		}
		if archive, err = aci.ArchiveFromTarGz(raw); err != nil {
			fail(err)
		}
		if _, _, err := signaturePolicy(archive, *allowUnsigned, *allowUntrusted); err != nil {
			fail(err)
		}
	} else {
		var err error
		if archive, err = loadArchiveFromDir(path); err != nil {
			fail(err)
		}
	}

	mem, err := runtime.OpenDefaultMemory(homeDir())
	if err != nil {
		fail(err)
	}
	defer mem.Close()
	ollama := llm.NewOllama("")
	if !ollama.IsAvailable() {
		fail(fmt.Errorf("Ollama not reachable at %s", ollama.BaseURL))
	}
	runner := &runtime.Runner{Archive: archive, Memory: mem, Ollama: ollama}
	if store, view := openTypedView(archive.Manifest.Metadata.Name); store != nil {
		defer store.Close()
		runner.Typed = view
	}
	if err := runner.Prepare(); err != nil {
		fail(err)
	}
	graph, err := archive.ParseSkillsGraph()
	if err != nil {
		fail(err)
	}
	var skills []string
	for _, s := range graph.Skills {
		skills = append(skills, s.Name)
	}
	trusted, _ := aci.DefaultTrustStore(homeDir()).List()

	srv := &amp.Server{
		Transport: amp.NewStdioTransport(),
		Trusted:   trusted,
		Skills:    skills,
		Handler: func(spec amp.DelegateSpec) (string, error) {
			q := spec.Intent
			if len(spec.Inputs) > 0 {
				b, _ := json.Marshal(spec.Inputs)
				q += "\n\nInputs: " + string(b)
			}
			out, err := runner.Ask(q)
			if runtime.AsMemoryWriteError(err) {
				// The task ran; the answer is good. Say the local log failed.
				fmt.Fprintf(os.Stderr, "amp: warning: %v\n", err)
				return out, nil
			}
			return out, err
		},
	}
	fmt.Fprintf(os.Stderr, "amp: serving %s@%s (skills: %s; %d trusted keys)\n",
		archive.Manifest.Metadata.Name, archive.Manifest.Metadata.Version, strings.Join(skills, ", "), len(trusted))
	if err := srv.Serve(context.Background()); err != nil {
		fail(err)
	}
}

func ampDelegate(args []string) {
	fs := flag.NewFlagSet("amp delegate", flag.ExitOnError)
	execCmd := fs.String("exec", "", "command that starts the peer, e.g. \"loomwork amp serve b.aci\" (no shell)")
	from := fs.String("from", "", "the .aci file you are delegating from (identifies the caller)")
	tokenPath := fs.String("token", "", "capability token file from the peer's owner or `loomwork amp token`")
	skill := fs.String("skill", "answer_question", "skill to run on the peer")
	timeout := fs.Duration("timeout", 5*time.Minute, "how long to wait for the result")
	pos := parseArgs(fs, args)
	intent := strings.Join(pos, " ")
	if *execCmd == "" || *from == "" || *tokenPath == "" || intent == "" {
		fail(fmt.Errorf("usage: loomwork amp delegate --exec CMD --from agent.aci --token token.json \"intent\""))
	}
	raw, err := os.ReadFile(*from)
	if err != nil {
		fail(err)
	}
	archive, err := aci.ArchiveFromTarGz(raw)
	if err != nil {
		fail(err)
	}
	tokData, err := os.ReadFile(*tokenPath)
	if err != nil {
		fail(err)
	}
	var tok amp.CapabilityToken
	if err := json.Unmarshal(tokData, &tok); err != nil {
		fail(fmt.Errorf("token file: %w", err))
	}
	sk := loadOrCreateKey(defaultKeyPath())
	prov := amp.ProvenanceFor(archive, raw, sk)

	parts := strings.Fields(*execCmd)
	if len(parts) == 0 {
		fail(fmt.Errorf("--exec is empty"))
	}
	tr, err := amp.NewExecTransport(exec.Command(parts[0], parts[1:]...))
	if err != nil {
		fail(fmt.Errorf("start peer: %w", err))
	}
	defer tr.Close()
	client := amp.NewClient(tr)
	client.SetTimeout(*timeout)

	taskID := amp.NewID("task")
	res, err := client.Delegate(&amp.DelegateParams{
		TaskID: taskID,
		Spec:   amp.DelegateSpec{Skill: *skill, Intent: intent, Inputs: map[string]any{}},
	}, prov, []amp.CapabilityToken{tok})
	if err != nil {
		fail(err)
	}
	if !res.Accepted {
		fail(fmt.Errorf("peer declined: %s", res.Reason))
	}
	rep, err := client.AwaitReport(*timeout)
	if err != nil {
		fail(err)
	}
	if rep.Status != "complete" {
		fail(fmt.Errorf("task %s: %s", rep.Status, rep.Error))
	}
	for _, a := range rep.Artifacts {
		if a.Inline != "" && aci.Sha256Bytes([]byte(a.Inline)) != a.Digest {
			fail(fmt.Errorf("result %q does not match its digest", a.Name))
		}
		fmt.Println(a.Inline)
	}
}
