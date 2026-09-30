package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"loomwork.dev/loomwork/internal/aci"
	"loomwork.dev/loomwork/internal/memory"
	"loomwork.dev/loomwork/internal/runtime"
)

// askOK reports whether an Ask succeeded well enough to show its answer. A
// *MemoryWriteError still carries a valid answer, so it is shown with a warning
// on stderr rather than dropped; any other error is a failure.
func askOK(err error) bool {
	if err == nil {
		return true
	}
	if runtime.AsMemoryWriteError(err) {
		fmt.Fprintf(os.Stderr, "⚠ %v\n", err)
		return true
	}
	return false
}

// parseArgs parses flags that may appear before, between or after positional
// arguments (Go's flag package stops at the first positional) and returns the
// positionals.
func parseArgs(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			os.Exit(1)
		}
		if fs.NArg() == 0 {
			return pos
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		fail(fmt.Errorf("cannot find home directory: %w", err))
	}
	return home
}

func defaultKeyPath() string {
	return filepath.Join(homeDir(), ".loomwork", "key.pem")
}

// loadOrCreateKey loads the Ed25519 signing key at path, creating it if it
// does not exist. The key's public half is added to the trust store: a key you
// generate yourself is one you trust.
func loadOrCreateKey(path string) *aci.SigningKey {
	var sk *aci.SigningKey
	if _, err := os.Stat(path); err == nil {
		sk, err = aci.LoadSigningKeyPEM(path)
		if err != nil {
			fail(err)
		}
	} else {
		fmt.Printf("No signing key at %s — generating one.\n", path)
		sk, err = aci.GenerateSigningKey()
		if err != nil {
			fail(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			fail(err)
		}
		if err := sk.SavePEM(path); err != nil {
			fail(err)
		}
		fmt.Printf("✓ Generated Ed25519 key: %s (key-id: %s)\n", path, sk.KeyID)
	}
	if err := aci.DefaultTrustStore(homeDir()).Add(sk.VerifyingKey()); err != nil {
		fail(fmt.Errorf("trust own key: %w", err))
	}
	return sk
}

// signaturePolicy decides whether an archive may be used. Missing or invalid
// signatures are refused unless allowUnsigned; a valid signature from a key
// the user has not chosen to trust is refused unless allowUntrusted.
func signaturePolicy(archive *aci.Archive, allowUnsigned, allowUntrusted bool) (aci.SignatureStatus, *aci.VerifyingKey, error) {
	ts := aci.DefaultTrustStore(homeDir())
	status, vk := ts.Check(archive)
	switch status {
	case aci.SigTrusted:
		return status, vk, nil
	case aci.SigValidUntrusted:
		if allowUntrusted || allowUnsigned {
			return status, vk, nil
		}
		return status, vk, fmt.Errorf("refusing: signed by key %s (fingerprint %s), which you have not trusted.\n"+
			"  If you trust the author, run: loomwork trust add <file.aci>\n"+
			"  or pass --allow-untrusted-signer to use it once", vk.KeyID, vk.Fingerprint())
	default:
		if allowUnsigned {
			return status, nil, nil
		}
		return status, nil, fmt.Errorf("refusing: %s (use --allow-unsigned to override)", status)
	}
}

// openTypedView opens the typed memory store and returns a view for agentName.
// It returns nil (and warns) if the store cannot be opened.
func openTypedView(agentName string) (*memory.Store, *memory.View) {
	store, err := memory.OpenDefaultStore(homeDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ typed memory unavailable: %v\n", err)
		return nil, nil
	}
	return store, memory.NewView(store, agentName, nil)
}
