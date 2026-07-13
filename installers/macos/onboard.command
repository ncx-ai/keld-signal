#!/bin/bash
# Keld setup — runs after install. Redeems your one-time setup code (from the Keld
# download page) for a non-interactive login, configures your AI tools, then starts
# the background agent. Safe to re-run.
set -uo pipefail
AGENT="/usr/local/bin/keld-agent"
echo; echo "==== Set up Keld ===="; echo
printf "Paste your setup code from the Keld download page (or press Enter to log in with a browser): "
read -r CODE
if [ -n "$CODE" ]; then
  # keld-agent install redeems the code (keld login --code), configures tools, then
  # starts the agent. Fall back to interactive install (browser login) if the code fails.
  "$AGENT" install --code "$CODE" || { echo "Setup code didn't work; falling back to browser login…"; "$AGENT" install || exit 1; }
else
  "$AGENT" install || exit 1
fi
echo; echo "Keld is set up and running. You can close this window."; echo
echo "(Re-run anytime: /usr/local/keld/onboard.command)"
