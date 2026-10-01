package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"loomwork.dev/loomwork/internal/shadow"
)

// Lane A (PRD §6.7.1): running loomwork in a folder that holds an AGENT.md
// keeps a record of the folder under ~/.loomwork/folders/. Nothing is written
// into the folder. Set LOOMWORK_NO_TRACK=1 to switch it off.

// trackFolder records dir's state. It never fails the calling command: a folder
// Lane A does not apply to is skipped silently, any other problem is a warning.
func trackFolder(dir string) {
	if os.Getenv("LOOMWORK_NO_TRACK") == "1" {
		return
	}
	s, err := shadow.Open(homeDir(), dir)
	if err != nil {
		warnTrack(err)
		return
	}
	res, err := s.Track()
	if err != nil {
		warnTrack(err)
		return
	}
	if res.First {
		fmt.Fprintf(os.Stderr, "ℹ Tracking this folder in %s. None of your files are touched or added to.\n"+
			"  See what changed: loomwork check. Stop tracking: loomwork leave.\n", s.Dir)
	} else if n := len(res.Changes); n > 0 {
		fmt.Fprintf(os.Stderr, "ℹ %d change(s) made outside Loomwork were recorded. Review: loomwork check\n", n)
	}
}

func warnTrack(err error) {
	if errors.Is(err, shadow.ErrNotApplicable) {
		return
	}
	fmt.Fprintf(os.Stderr, "⚠ folder tracking skipped: %v\n", err)
}

// openTracked resolves the folder argument for check and seal.
func openTracked(cmd string, args []string) *shadow.Store {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	pos := parseArgs(fs, args)
	dir := "."
	if len(pos) > 1 {
		fail(fmt.Errorf("usage: loomwork %s [dir]", cmd))
	}
	if len(pos) == 1 {
		dir = pos[0]
	}
	s, err := shadow.Open(homeDir(), dir)
	if errors.Is(err, shadow.ErrNotApplicable) {
		fail(fmt.Errorf("%s is not a folder Loomwork tracks (needs a regular AGENT.md, and not your home or /)", dir))
	} else if err != nil {
		fail(err)
	}
	return s
}

// cmdCheck: loomwork check [dir]
//
// Compares the folder with the last state you sealed and verifies the journal
// chain. Exits non-zero on any difference. Recording changes is automatic;
// acknowledging them is not (see seal).
func cmdCheck(args []string) {
	s := openTracked("check", args)
	if !s.Tracked() {
		fail(fmt.Errorf("%s is not tracked yet: run `loomwork seal` to take a first snapshot", s.Folder))
	}
	trackFolder(s.Folder)
	rep, err := s.Check()
	if err != nil {
		fail(err)
	}
	for _, c := range rep.Drift {
		fmt.Printf("  %-8s %s\n", c.Op, c.File)
	}
	if rep.ChainBroken > 0 {
		fmt.Printf("  journal chain broken at line %d\n", rep.ChainBroken)
	}
	if rep.HeadMismatch {
		fmt.Println("  journal does not end where the store says it should (lines removed or altered)")
	}
	if !rep.OK() {
		fmt.Printf("✗ %s differs from the last sealed state. If the changes are yours, run: loomwork seal\n", s.Folder)
		os.Exit(1)
	}
	fmt.Printf("✓ %s matches the last sealed state; journal intact\n", s.Folder)
}

// cmdSeal: loomwork seal [dir]
//
// Acknowledges the folder's current state. Always an explicit command.
func cmdSeal(args []string) {
	s := openTracked("seal", args)
	trackFolder(s.Folder)
	n, err := s.Seal()
	if err != nil {
		fail(err)
	}
	fmt.Printf("✓ Sealed %d file(s) in %s\n", n, s.Folder)
}
