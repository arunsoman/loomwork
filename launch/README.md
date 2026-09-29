# Recording the demo

`go/scripts/demo-full.sh` runs the complete sequence on one machine using two separate home directories to stand in for a laptop and a phone: package an agent, copy it (with its memory and key) to the second home, verify and run it there, and inspect the attestation.

```bash
cd go
go build -o loomwork .
./scripts/demo-full.sh
```

To record it, use asciinema or a screen recorder. `go/docs/demo-on-phone.md` covers running the second half on a real phone with Termux.
