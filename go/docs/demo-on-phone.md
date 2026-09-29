# Running the demo on a real phone (Termux)

The 25-second demo's killer beat is "I packaged my research agent on my laptop, ran the same .aci on my phone, it remembered the conversation." Here's how to actually do that.

## On the phone (Android, via Termux)

```bash
# 1. Install Termux from F-Droid (Google Play version is outdated)
# 2. In Termux:
pkg update && pkg install wget
# 3. Install Ollama (ARM64 build)
curl -fsSL https://ollama.com/install.sh | sh
# 4. Pull a small model (phone-tier)
ollama pull qwen2.5:3b   # or llama3.2:3b
# 5. Install loomwork (ARM64 binary)
curl -fsSL https://raw.githubusercontent.com/arunsoman/loomwork/main/go/install.sh | sh
#    (or manually: wget https://github.com/loomwork/loomwork/releases/latest/download/loomwork-linux-arm64 -O $PREFIX/bin/loomwork && chmod +x $PREFIX/bin/loomwork)
# 6. Check it works
loomwork doctor
```

## On the laptop

```bash
# 1. Build your agent
loomwork init research-agent
cd research-agent
loomwork ask "what is the ACI spec?" --folder ~/research
loomwork package --out research.aci

# 2. Copy the .aci + memory DB to the phone
#    (Termux exposes its filesystem at ~/storage/shared/)
scp research.aci phone:~/storage/shared/
scp ~/.loomwork/memory.db phone:~/.loomwork/

# 3. On the phone, run it
#    (SSH in or use Termux directly)
loomwork verify research.aci       # signature still valid
loomwork run research.aci          # loads, prints "Memory has: ..."
```

## Why this works

- The `.aci` is a single signed file — copy it anywhere, it just works.
- Memory is a single SQLite DB — copy it alongside the .aci, the agent remembers.
- The binary is statically linked — no library dependencies, runs on any Linux/ARM64 (including Termux's proot'd environment).
- The spec is wire-compatible — Go binary on laptop, Go binary on phone, same .aci format.

## What's NOT yet supported (v0.1)

- **Automatic sync.** You have to manually copy the memory DB. Multi-device P2P sync (libp2p) is deferred until the format has traction.
- **iOS.** Apple doesn't allow proot'd Linux binaries. A native Swift port is the path; not in v0.1.
- **Concurrent writes.** If you run the agent on two devices simultaneously with the same memory DB, last-write-wins. Sync server is the fix; deferred.

## Recording the demo

1. Set up phone + laptop side-by-side.
2. Use OBS or QuickTime to screen-record both.
3. Run the beats:
   - Laptop: `loomwork init`, `loomwork ask`, `loomwork package`
   - Show the .aci file size (it's tiny — ~2 KB)
   - Copy to phone (show the `scp` command)
   - Phone: `loomwork run` — show the "Memory has:" line
   - Phone: `loomwork receipt` — show the attestation
4. Trim to 25 seconds. Add a voiceover: *"Packaged on laptop. Ran on phone. It remembered. Receipt."*
5. Share the recording.
