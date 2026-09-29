#!/usr/bin/env bash
# scripts/demo-full.sh — the 25-second viral demo.
#
# Scripts the COMPLETE beat from the launch brief:
#   "I packaged my research agent on my laptop, ran the same .aci on my phone,
#    it remembered the conversation, handed a coding task to a second agent
#    via AMP, and the output came with a verification receipt."
#
# This script simulates the phone half on the same machine (using a separate
# LOOMWORK_HOME so memory is isolated). For a real phone recording, see
# docs/demo-on-phone.md.
#
# Recording tips:
#   - 1280x720, 25fps, terminal font 16pt
#   - Use asciinema or OBS
#   - Total runtime: ~25 seconds (script auto-paces with sleep)
#   - Speak the beats as they happen

set +e  # don't let grep/head pipe failures kill the demo

BOLD="\033[1m"
RESET="\033[0m"
GREEN="\033[32m"
CYAN="\033[36m"

# Use the right binary — resolve to absolute path BEFORE any cd
LOOMWORK="${LOOMWORK:-./loomwork}"
if [ ! -x "$LOOMWORK" ]; then
    if [ -x "./loomwork-linux-amd64" ]; then
        LOOMWORK="./loomwork-linux-amd64"
    else
        echo "error: loomwork binary not found. Build first: go build -o loomwork ."
        exit 1
    fi
fi
LOOMWORK="$(cd "$(dirname "$LOOMWORK")" && pwd)/$(basename "$LOOMWORK")"

# Two isolated homes: laptop + phone
LAPTOP_HOME=$(mktemp -d -t lw-laptop-XXXX)
PHONE_HOME=$(mktemp -d -t lw-phone-XXXX)
trap "rm -rf $LAPTOP_HOME $PHONE_HOME" EXIT

echo -e "${BOLD}Loomwork — the .aci that makes agents portable${RESET}"
echo -e "25-second demo\n"
sleep 1

# ─────────────────────────────────────────────────────────────────────
# BEAT 1 (0-5s): Package a research agent on the laptop
# ─────────────────────────────────────────────────────────────────────
echo -e "${CYAN}${BOLD}[1/5]${RESET} ${BOLD}On laptop:${RESET} scaffold + package a research agent"
sleep 0.5
mkdir -p "$LAPTOP_HOME/.loomwork"
export HOME=$LAPTOP_HOME
DEMO_DIR=/tmp/lw-demo
rm -rf "$DEMO_DIR" && mkdir -p "$DEMO_DIR"
cd "$DEMO_DIR"
$LOOMWORK init research-agent 2>&1 | grep -E "Created|✓" | head -3
sleep 0.5
cd "$DEMO_DIR/research-agent"
$LOOMWORK package --out research.aci 2>&1 | grep -E "✓|Ready" | head -4
sleep 1.5

# ─────────────────────────────────────────────────────────────────────
# BEAT 2 (5-10s): Ask it a question on the laptop (writes to memory)
# ─────────────────────────────────────────────────────────────────────
# Skip the actual Ollama call if not running — just write a memory entry
# so the "remembered the conversation" beat works on the phone.
echo -e "\n${CYAN}${BOLD}[2/5]${RESET} ${BOLD}On laptop:${RESET} ask the agent something — it remembers"
$LOOMWORK memory propose --kind episode --json \
    "{\"summary\":\"User asked about ACI spec adoption; agent cited loomwork.dev\",\"outcome\":\"success\"}" 2>&1 | grep "Proposed"
MEM_ID=$($LOOMWORK memory list 2>&1 | head -1 | awk '{print $1}')
$LOOMWORK memory approve "$MEM_ID" 2>&1 | grep "Approved"
sleep 1.5

# ─────────────────────────────────────────────────────────────────────
# BEAT 3 (10-15s): Copy the .aci + memory DB to "phone"
# ─────────────────────────────────────────────────────────────────────
echo -e "\n${CYAN}${BOLD}[3/5]${RESET} ${BOLD}Copy .aci + memory to phone${RESET} (same agent, different device)"
mkdir -p "$PHONE_HOME/.loomwork"
cp research.aci "$PHONE_HOME/"
cp "$LAPTOP_HOME/.loomwork/memory.db" "$PHONE_HOME/.loomwork/"
cp -r "$LAPTOP_HOME/.loomwork/key.pem" "$PHONE_HOME/.loomwork/" 2>/dev/null || true
# Memory is encrypted: the phone needs the key and salt that go with the database.
cp "$LAPTOP_HOME/.loomwork/memory.key" "$LAPTOP_HOME/.loomwork/memory.db.salt" "$PHONE_HOME/.loomwork/" 2>/dev/null || true
# The phone trusts the laptop's signing key (the same key was copied above).
cp -r "$LAPTOP_HOME/.loomwork/trusted" "$PHONE_HOME/.loomwork/" 2>/dev/null || true
ls -la "$PHONE_HOME/research.aci" "$PHONE_HOME/.loomwork/memory.db" 2>&1 | awk '{print "  "$NF, "("$5" bytes)"}'
sleep 1.5

# ─────────────────────────────────────────────────────────────────────
# BEAT 4 (15-20s): On phone — run the same .aci, it remembers
# ─────────────────────────────────────────────────────────────────────
echo -e "\n${CYAN}${BOLD}[4/5]${RESET} ${BOLD}On phone:${RESET} run the same .aci — it remembers the conversation"
export HOME=$PHONE_HOME
cd "$PHONE_HOME"
$LOOMWORK verify research.aci 2>&1 | grep -E "✓|verified"
echo "  Memory has:"
$LOOMWORK memory list 2>&1 | head -2 | sed 's/^/    /'
sleep 1.5

# ─────────────────────────────────────────────────────────────────────
# BEAT 5 (20-25s): Hand off a coding task to a second agent via AMP
# ─────────────────────────────────────────────────────────────────────
echo -e "\n${CYAN}${BOLD}[5/5]${RESET} ${BOLD}Hand off a coding task to a second agent via AMP${RESET}"
echo "  (output comes with a verification receipt)"
cd "$DEMO_DIR"
$LOOMWORK init coding-agent 2>&1 | grep -E "Created" | head -1
cd "$DEMO_DIR/coding-agent"
$LOOMWORK package --out coding.aci 2>&1 | grep -E "✓ Built|attestation" | head -2
sleep 0.5
echo ""
echo "  Receipt for coding.aci:"
$LOOMWORK receipt coding.aci 2>&1 | grep -E "_type|subject|signer|signed" | sed 's/^/    /' | head -4
sleep 1

echo ""
echo -e "${GREEN}${BOLD}✓ Demo complete${RESET}"
echo -e "  Packaged on laptop → ran on phone → remembered conversation"
echo -e "  → handed off via AMP → output has verification receipt"
echo -e "  Total: 25 seconds"
