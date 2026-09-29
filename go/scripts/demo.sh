#!/usr/bin/env bash
# 25-second demo script.
#
# Records the killer path: init → ask → package → run.
# Run this while screen-recording to produce the launch asset.
#
# Prereqs:
#   - loomwork binary on PATH
#   - Ollama running with llama3.2 pulled
#   - A sample folder with some files (this script creates one)
#
# Usage:
#   ./scripts/demo.sh
#
# Recommended recording: 1280x720, 25fps, ~25 seconds total runtime.

set -e

BOLD="\033[1m"
RESET="\033[0m"
GREEN="\033[32m"

# Cleanup any prior demo
DEMO_DIR=$(mktemp -d -t loomwork-demo-XXXXXX)
trap "rm -rf $DEMO_DIR" EXIT

echo -e "${BOLD}Loomwork 25-second demo${RESET}"
echo -e "Working in: $DEMO_DIR\n"
sleep 1

# Step 1: create a sample folder to point the agent at
echo -e "${BOLD}[1/5]${RESET} Setting up sample folder..."
mkdir -p "$DEMO_DIR/docs"
cat > "$DEMO_DIR/docs/readme.md" << 'EOF'
# Project Notes

This is a sample project for the Loomwork demo.
It contains research notes and a TODO list.

## TODO
- [ ] Ship v0.1
- [ ] Write more tests
EOF
cat > "$DEMO_DIR/notes.txt" << 'EOF'
Remember to follow up with the team about the ACI spec.
EOF
sleep 1

# Step 2: init
echo -e "\n${BOLD}[2/5]${RESET} loomwork init my-agent"
cd "$DEMO_DIR"
loomwork init my-agent
sleep 2

# Step 3: ask
echo -e "\n${BOLD}[3/5]${RESET} loomwork ask \"what does this folder contain?\" --folder ."
cd my-agent
loomwork ask "what does this folder contain? summarize the project." --folder "$DEMO_DIR"
sleep 3

# Step 4: package
echo -e "\n${BOLD}[4/5]${RESET} loomwork package --out my-agent.aci"
loomwork package --out my-agent.aci
sleep 2

# Step 5: run
echo -e "\n${BOLD}[5/5]${RESET} loomwork run my-agent.aci --input 'what did we just discuss?'"
loomwork run my-agent.aci --input 'what did we just discuss?'
sleep 2

echo -e "\n${GREEN}${BOLD}✓ Demo complete${RESET}"
echo -e "Total time: ~25 seconds"
echo -e "Artifacts:"
ls -la "$DEMO_DIR/my-agent/my-agent.aci" "$DEMO_DIR/my-agent/my-agent.slsa.json" 2>/dev/null
