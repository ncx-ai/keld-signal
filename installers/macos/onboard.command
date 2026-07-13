#!/bin/bash
# Keld setup — runs after install. Redeems your one-time setup code (from the Keld
# download page) for a non-interactive login, configures your AI tools, then starts
# the background agent. Safe to re-run.
set -uo pipefail
KELD="/usr/local/bin/keld"; AGENT="/usr/local/bin/keld-agent"
echo; echo "==== Set up Keld ===="; echo
printf "Paste your setup code from the Keld download page (or press Enter to log in with a browser): "
read -r CODE
if [ -n "$CODE" ]; then
  "$KELD" login --code "$CODE" || { echo "Setup code didn't work; falling back to browser login…"; "$KELD" login || exit 1; }
else
  "$KELD" login || exit 1
fi
"$KELD" signal setup --yes || exit 1
"$AGENT" install || exit 1
echo; echo "Keld is set up and running. You can close this window."; echo
echo "(Re-run anytime: /usr/local/keld/onboard.command)"
