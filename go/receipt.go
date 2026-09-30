package main

import (
	"encoding/json"
	"fmt"
	"os"

	"loomwork.dev/loomwork/internal/aci"
	"loomwork.dev/loomwork/internal/verify"
)

// loadAttestation reads <aci>.attestation.json and checks that it is signed by
// the ACI's signer, refers to exactly these ACI bytes, and (for the returned
// trusted flag) that the signer is a trusted key.
func loadAttestation(aciPath string, raw []byte, archive *aci.Archive) (att *verify.Attestation, trusted bool, err error) {
	data, err := os.ReadFile(aciPath + ".attestation.json")
	if err != nil {
		return nil, false, fmt.Errorf("no attestation sidecar")
	}
	att = &verify.Attestation{}
	if err := json.Unmarshal(data, att); err != nil {
		return nil, false, fmt.Errorf("attestation unreadable")
	}
	vk, err := aci.ArchiveSigner(archive)
	if err != nil {
		return att, false, fmt.Errorf("ACI has no signer key")
	}
	if !att.Verify(vk.Pub) {
		return att, false, fmt.Errorf("attestation signature is invalid")
	}
	if att.ArchiveDigest != aci.Sha256Bytes(raw) {
		return att, false, fmt.Errorf("attestation is for a different archive")
	}
	if want := archive.Manifest.Metadata.Name + "@" + archive.Manifest.Metadata.Version; att.Subject != want {
		return att, false, fmt.Errorf("attestation subject %q does not match this agent (%s)", att.Subject, want)
	}
	return att, aci.DefaultTrustStore(homeDir()).IsTrusted(vk), nil
}

// printVerificationReceipt prints a receipt line for `loomwork run`. The
// attestation is only shown as valid after its signature and archive digest
// have been checked.
func printVerificationReceipt(aciPath string, raw []byte, archive *aci.Archive) {
	att, trusted, err := loadAttestation(aciPath, raw, archive)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  receipt: none (%v)\n", err)
		return
	}
	passed := "PASSED"
	if !att.PropertyTests.Passed {
		passed = "FAILED"
	}
	who := "trusted signer"
	if !trusted {
		who = "UNTRUSTED signer"
	}
	fmt.Fprintf(os.Stderr, "  receipt: %s  checks=%s  signer=%s (%s)  signed=%s\n",
		aci.Sha256Bytes(raw)[:23], passed, att.Signer, who, att.SignedAt)
}

// cmdReceipt prints or verifies the attestation for an ACI.
//
//	loomwork receipt agent.aci            # print it
//	loomwork receipt agent.aci --verify   # check signature and archive digest
func cmdReceipt(args []string) {
	verifyMode := false
	var rest []string
	for _, a := range args {
		if a == "--verify" {
			verifyMode = true
		} else {
			rest = append(rest, a)
		}
	}
	if len(rest) != 1 {
		fail(fmt.Errorf("usage: loomwork receipt <agent.aci> [--verify]"))
	}
	aciPath := rest[0]
	raw, err := os.ReadFile(aciPath)
	if err != nil {
		fail(err)
	}
	archive, err := aci.ArchiveFromTarGz(raw)
	if err != nil {
		fail(err)
	}
	att, trusted, err := loadAttestation(aciPath, raw, archive)
	if err != nil {
		fail(err)
	}
	if verifyMode {
		note := ""
		if !trusted {
			note = " (signer is not in your trusted keys)"
		}
		fmt.Printf("✓ Attestation valid for this archive (signer: %s)%s\n", att.Signer, note)
		return
	}
	b, _ := json.MarshalIndent(att, "", "  ")
	fmt.Println(string(b))
}
