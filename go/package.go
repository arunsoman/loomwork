package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"loomwork.dev/loomwork/internal/aci"
	"loomwork.dev/loomwork/internal/verify"
)

// cmdPackage builds a signed .aci from the current directory, with a signed
// provenance statement and a signed SBOM + structural-check attestation.
//
// Usage:
//
//	loomwork package [--signing-key key.pem] [--out agent.aci]
//
// If --signing-key is omitted, ~/.loomwork/key.pem is used (and created if
// missing). Only files named in the manifest are packed.
//
// In a plain folder (AGENT.md, no manifest.json) nothing is written into the
// folder except the archive and its sidecars: the manifest is derived fresh on
// every run, so it can never be stale. `loomwork init` opts into native mode.
func cmdPackage(args []string) {
	fs := flag.NewFlagSet("package", flag.ExitOnError)
	keyPath := fs.String("signing-key", "", "PEM file with Ed25519 private key (default: ~/.loomwork/key.pem)")
	out := fs.String("out", "", "output .aci path (default: <agent-name>.aci)")
	withSBOM := fs.Bool("with-sbom", true, "write the SBOM + structural-check attestation sidecar")
	parseArgs(fs, args)
	sourceDir := "."
	// buildDir is where the manifest and signature are generated. For a native
	// folder (it has manifest.json) that is the folder itself; for a plain
	// folder it is a temporary staging directory, so nothing but the archive
	// and its sidecars is ever written into the user's folder.
	buildDir := sourceDir
	plain := isPlainFolder(sourceDir, false)
	if plain {
		stage, err := stageFolder(sourceDir)
		if err != nil {
			fail(err)
		}
		defer os.RemoveAll(stage)
		buildDir = stage
	} else if isFolderAgent(sourceDir) {
		// Folder-built agent, packaged again: pick up skills added since init.
		if err := refreshFolderSkills(sourceDir); err != nil {
			fail(err)
		}
	}

	if *keyPath == "" {
		*keyPath = defaultKeyPath()
	}
	sk := loadOrCreateKey(*keyPath)

	// Re-compute digests (files may have changed since init)
	manifest, err := recomputeDigests(buildDir)
	if err != nil {
		fail(err)
	}

	if !plain {
		warnUnlistedFiles(sourceDir, manifest.Digests, *out)
	}

	// The skills graph must be acyclic and its implementations present.
	if err := aci.DetectCyclesInDirectory(buildDir); err != nil {
		fail(fmt.Errorf("skills graph validation failed: %w", err))
	}
	fmt.Printf("✓ Skills graph validated (no cycles)\n")

	if err := aci.SignArchiveInPlace(buildDir, sk); err != nil {
		fail(err)
	}
	fmt.Printf("✓ Signed: signatures/manifest.sig (key-id: %s)\n", sk.KeyID)

	// Pack (only manifest-listed files and the signature)
	archive, err := aci.ArchiveFromDirectory(buildDir)
	if err != nil {
		fail(err)
	}
	data, err := archive.ToTarGz()
	if err != nil {
		fail(err)
	}
	outPath := *out
	if outPath == "" {
		outPath = manifest.Metadata.Name + ".aci"
	}
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("✓ Built ACI: %s (%d bytes, %d files)\n", outPath, len(data), len(archive.Files))
	archiveDigest := aci.Sha256Bytes(data)

	// Signed provenance statement
	absSrc, _ := filepath.Abs(sourceDir)
	slsa := aci.BuildSLSAAttestation(manifest, archiveDigest, absSrc)
	if err := aci.SignSLSA(slsa, sk); err != nil {
		fail(err)
	}
	slsaPath := aci.SLSASidecarPath(outPath)
	if err := aci.SaveSLSA(slsa, slsaPath); err != nil {
		fail(err)
	}
	fmt.Printf("✓ Wrote signed provenance: %s\n", slsaPath)

	// Signed SBOM + structural checks, bound to this exact archive
	if *withSBOM {
		sbom, err := verify.BuildSBOM(buildDir, manifest)
		if err != nil {
			fail(err)
		}
		tests := verify.RunPropertyTests(buildDir, manifest)
		att, err := verify.BuildAttestation(sbom, tests, archiveDigest, sk.KeyID, sk.Priv)
		if err != nil {
			fail(err)
		}
		attPath := outPath + ".attestation.json"
		if err := verify.SaveAttestation(att, attPath); err != nil {
			fail(err)
		}
		status := "PASSED"
		if !tests.Passed {
			status = "FAILED"
		}
		fmt.Printf("✓ Wrote attestation: %s (structural checks: %s)\n", attPath, status)
		if !tests.Passed {
			b, _ := json.MarshalIndent(tests, "", "  ")
			fmt.Fprintln(os.Stderr, string(b))
		}
	}

	fmt.Printf("\nReady to share: %s + %s\n", outPath, slsaPath)
	fmt.Printf("Run it: loomwork run %s\n", outPath)
}

// recomputeDigests recomputes the digest of every file the manifest names
// (persona, skills graph, tool bindings, memory schema, sandbox spec and any
// skill implementation files) and writes the updated manifest.
func recomputeDigests(sourceDir string) (*aci.Manifest, error) {
	manifestPath := filepath.Join(sourceDir, "manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var manifest aci.Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, err
	}
	paths := []string{
		manifest.Persona.SystemPrompt,
		manifest.Skills.Graph,
		manifest.Tools.Bindings,
		manifest.Memory.Schema,
		manifest.Sandbox.Spec,
	}
	for _, p := range paths {
		if p == "" {
			return nil, fmt.Errorf("manifest is missing a required file reference")
		}
	}
	if manifest.Persona.FewShot != "" {
		// Optional persona file: digest it (and so pack and sign it) when declared.
		paths = append(paths, manifest.Persona.FewShot)
	}
	// Skill implementation files are part of the agent and must be covered too.
	if graphData, err := os.ReadFile(filepath.Join(sourceDir, manifest.Skills.Graph)); err == nil {
		var g aci.SkillsGraph
		if json.Unmarshal(graphData, &g) == nil {
			paths = append(paths, g.ImplEntries()...)
		}
	}
	manifest.Digests = map[string]string{}
	for _, p := range paths {
		if err := aci.ValidateEntryName(p); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(filepath.Join(sourceDir, filepath.FromSlash(p)))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		manifest.Digests[p] = aci.Sha256Bytes(data)
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	out, err := manifest.ToJSON()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(manifestPath, out, 0o644); err != nil {
		return nil, err
	}
	return &manifest, nil
}
