package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"loomwork.dev/loomwork/internal/aci"
)

// cmdTrust manages the keys whose signatures loomwork accepts.
//
//	loomwork trust list
//	loomwork trust add <file.aci | key.pub.pem>
//	loomwork trust remove <key-id>
func cmdTrust(args []string) {
	if len(args) < 1 {
		fmt.Print(`loomwork trust — choose whose signatures to accept

  loomwork trust list                     Show trusted keys
  loomwork trust add <agent.aci|key.pem>  Trust the key that signed an ACI (or a public key file)
  loomwork trust remove <key-id>          Stop trusting a key

Keys you generate (loomwork keygen / package) are trusted automatically.
`)
		return
	}
	ts := aci.DefaultTrustStore(homeDir())
	switch args[0] {
	case "list":
		keys, err := ts.List()
		if err != nil {
			fail(err)
		}
		if len(keys) == 0 {
			fmt.Println("No trusted keys.")
		}
		for _, k := range keys {
			fmt.Printf("  %s  sha256:%s\n", k.KeyID, k.Fingerprint())
		}
	case "add":
		fs := flag.NewFlagSet("trust add", flag.ExitOnError)
		yes := fs.Bool("yes", false, "do not ask for confirmation")
		pos := parseArgs(fs, args[1:])
		if len(pos) != 1 {
			fail(fmt.Errorf("usage: loomwork trust add <agent.aci | key.pem> [--yes]"))
		}
		data, err := os.ReadFile(pos[0])
		if err != nil {
			fail(err)
		}
		var vk *aci.VerifyingKey
		if strings.HasSuffix(pos[0], ".aci") {
			archive, err := aci.ArchiveFromTarGz(data)
			if err != nil {
				fail(err)
			}
			if !aci.VerifyArchiveSignature(archive) {
				fail(fmt.Errorf("%s has no valid signature; nothing to trust", pos[0]))
			}
			vk, err = aci.ArchiveSigner(archive)
			if err != nil {
				fail(err)
			}
			fmt.Printf("%s@%s is signed by:\n", archive.Manifest.Metadata.Name, archive.Manifest.Metadata.Version)
		} else {
			vk, err = aci.LoadVerifyingKeyPEMBytes(data)
			if err != nil {
				fail(err)
			}
		}
		fmt.Printf("  key id:      %s\n  fingerprint: sha256:%s\n", vk.KeyID, vk.Fingerprint())
		if !*yes {
			fmt.Print("Trust this key? Only say yes if you have verified the fingerprint with its owner [y/N]: ")
			var ans string
			fmt.Scanln(&ans)
			if ans != "y" && ans != "Y" && ans != "yes" {
				fmt.Println("Not trusted.")
				os.Exit(1)
			}
		}
		if err := ts.Add(vk); err != nil {
			fail(err)
		}
		fmt.Printf("✓ Trusted %s\n", vk.KeyID)
	case "remove":
		if len(args) != 2 {
			fail(fmt.Errorf("usage: loomwork trust remove <key-id>"))
		}
		if err := ts.Remove(args[1]); err != nil {
			fail(err)
		}
		fmt.Printf("✓ No longer trusting %s\n", args[1])
	default:
		fail(fmt.Errorf("unknown trust subcommand %q", args[0]))
	}
}
