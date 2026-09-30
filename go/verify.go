package main

import (
	"flag"
	"fmt"
	"os"

	"loomwork.dev/loomwork/internal/aci"
)

// cmdVerify verifies an ACI's digests, schema, signature (and that the signer
// is trusted) and its provenance statement. It exits non-zero unless the
// signature is valid and from a trusted key.
//
// Usage: loomwork verify [--allow-untrusted-signer] agent.aci
func cmdVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	allowUntrusted := fs.Bool("allow-untrusted-signer", false, "accept a valid signature from a key you have not trusted")
	pos := parseArgs(fs, args)
	if len(pos) != 1 {
		fail(fmt.Errorf("usage: loomwork verify [--allow-untrusted-signer] <agent.aci>"))
	}
	aciPath := pos[0]
	data, err := os.ReadFile(aciPath)
	if err != nil {
		fail(err)
	}
	archive, err := aci.ArchiveFromTarGz(data)
	if err != nil {
		fail(fmt.Errorf("parse ACI: %w", err))
	}
	fmt.Printf("✓ All %d file digests verified\n", len(archive.Manifest.Digests))
	fmt.Printf("✓ Manifest schema valid\n")

	if err := archive.ValidateSkillsGraph(); err != nil {
		fmt.Printf("✗ Skills graph invalid: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Skills graph valid\n")

	status, vk, sigErr := signaturePolicy(archive, false, *allowUntrusted)
	switch status {
	case aci.SigTrusted:
		fmt.Printf("✓ Manifest signature valid, signer %s is trusted\n", vk.KeyID)
	case aci.SigValidUntrusted:
		fmt.Printf("✓ Manifest signature valid, but signer %s is NOT trusted (fingerprint sha256:%s)\n", vk.KeyID, vk.Fingerprint())
	case aci.SigInvalid:
		fmt.Printf("✗ Manifest signature INVALID\n")
	default:
		fmt.Printf("✗ No signature present\n")
	}

	if sigErr != nil {
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", sigErr)
		os.Exit(1)
	}

	slsaPath := aci.SLSASidecarPath(aciPath)
	switch slsa, err := aci.LoadSLSA(slsaPath); {
	case err != nil:
		fmt.Printf("⚠ No provenance statement (sidecar %s not found)\n", slsaPath)
	case vk != nil && aci.VerifySLSASigned(slsa, aci.Sha256Bytes(data), vk):
		fmt.Printf("✓ Provenance statement valid and signed by the ACI signer\n")
	default:
		fmt.Printf("✗ Provenance statement does not match this archive or signer\n")
		os.Exit(1)
	}

	fmt.Printf("\nACI verified: %s@%s\n", archive.Manifest.Metadata.Name, archive.Manifest.Metadata.Version)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
