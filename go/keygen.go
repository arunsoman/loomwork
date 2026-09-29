package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"loomwork.dev/loomwork/internal/aci"
)

// cmdKeygen generates an Ed25519 signing key and writes it as PKCS8 PEM.
//
// Usage: loomwork keygen [--out key.pem]
func cmdKeygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "", "output PEM path (default: ~/.loomwork/key.pem)")
	parseArgs(fs, args)
	outPath := *out
	if outPath == "" {
		home, _ := os.UserHomeDir()
		outPath = filepath.Join(home, ".loomwork", "key.pem")
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o700); err != nil {
		fail(err)
	}
	sk, err := aci.GenerateSigningKey()
	if err != nil {
		fail(err)
	}
	if err := sk.SavePEM(outPath); err != nil {
		fail(err)
	}
	fmt.Printf("✓ Generated Ed25519 key: %s\n", outPath)
	fmt.Printf("  Key ID: %s\n", sk.KeyID)
	pubPath := outPath + ".pub"
	if err := sk.VerifyingKey().SavePEM(pubPath); err != nil {
		fail(err)
	}
	fmt.Printf("  Public key: %s\n", pubPath)
	if err := aci.DefaultTrustStore(homeDir()).Add(sk.VerifyingKey()); err != nil {
		fail(err)
	}
	fmt.Printf("  Trusted: yes (keys you generate are trusted automatically)\n")
}
