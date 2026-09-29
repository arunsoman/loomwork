// Package main is the loomwork CLI entrypoint.
//
// Loomwork is a single-binary agent runtime that ships the ACI (Agent
// Container Image) format and a thin AMP (Agent Mesh Protocol) delegate.
//
// Usage:
//
//	loomwork init                     # scaffold a minimal agent in the current dir
//	loomwork ask "what is in here?"   # point at a folder, get answers
//	loomwork package                  # emit a signed .aci
//	loomwork run other-agent.aci      # load and run an ACI
//	loomwork verify foo.aci           # verify signature + SLSA
//	loomwork keygen                   # generate an Ed25519 signing key
//	loomwork version
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "init":
		cmdInit(args)
	case "ask":
		cmdAsk(args)
	case "package", "pkg":
		cmdPackage(args)
	case "run":
		cmdRun(args)
	case "verify":
		cmdVerify(args)
	case "keygen":
		cmdKeygen(args)
	case "memory", "mem":
		cmdMemory(args)
	case "doctor":
		cmdDoctor(args)
	case "receipt":
		cmdReceipt(args)
	case "trust":
		cmdTrust(args)
	case "amp":
		cmdAmp(args)
	case "version", "--version", "-v":
		cmdVersion()
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`loomwork — portable personal agents, single binary

Usage:
  loomwork init [dir]                  Scaffold a minimal agent (default: .)
  loomwork ask "question" [--folder .] Ask your local agent about a folder
  loomwork package [--out agent.aci]   Emit a signed .aci from the current dir
  loomwork run agent.aci [--input '...']
                                       Load and run an ACI locally
  loomwork verify agent.aci            Verify digests + signature + trusted signer + provenance
  loomwork keygen [--out key.pem]      Generate an Ed25519 signing key
  loomwork trust list|add|remove       Choose whose signatures to accept
  loomwork amp serve|token|delegate    Hand a task to another agent over stdio

  loomwork memory list|get|propose|approve|revoke|graph|stats
                                       Manage typed memory (preferences, episodes,
                                       artifacts, beliefs, failures) with
                                       provenance, consent, retention, revocation
  loomwork doctor                      Check your setup — one command, no config

  loomwork version                     Print version

The <60-second path:
  loomwork doctor                      # check everything works
  loomwork init my-agent
  cd my-agent
  loomwork ask "what does this folder contain?" --folder ~/Documents
  loomwork package --out my-agent.aci
  loomwork run my-agent.aci --input '{"task":"hello"}'

Memory contract (v0.2 — typed records):
  5 kinds: preference, episode, artifact, belief, failure
  Every record has: provenance, consent scope, retention, sensitivity
  Agents propose; users (or policy) approve before records become durable
  Revoke deletes content and cascades to derivatives and beliefs citing it

Default behaviors:
  - Local LLM via Ollama (localhost:11434). Set OLLAMA_URL to override.
  - Memory stored at ~/.loomwork/memory.db (SQLite, AES-256-GCM encrypted; key in ~/.loomwork/memory.key).
  - No telemetry, no phone-home. A cloud model is used only if no local model is installed,
    and then only the file listing (not contents) is sent unless you pass --allow-cloud-samples.
  - Zero required config. Run 'loomwork doctor' if anything breaks.

Spec: loomwork.dev/v0.1   |   License: Apache-2.0
`)
}
