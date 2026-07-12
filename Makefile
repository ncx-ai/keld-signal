# keld-cli — local dev install + enrichment visualization.
# `make help` for the target list. Signed release installers are built in CI
# (see .github/workflows/installers.yml); these targets are for local dev.
SHELL := /bin/bash
DEST ?= $(HOME)/.local/bin
SIDECAR_VENV ?= $(HOME)/.keld/sidecar-venv
# sidecar needs Python 3.12 (host default 3.14 has no torch/gliner2 wheels)
PYTHON ?= python3.12
SINK_PORT ?= 8710

.DEFAULT_GOAL := help

.PHONY: help
help:
	@echo "keld-cli local dev targets:"
	@echo "  make install-linux      build keld + keld-agent + sidecar and install the systemd --user service"
	@echo "  make uninstall-linux    stop + remove the service"
	@echo "  make enrichments-sink   run a local sink that prints enrichments as they are generated"
	@echo "  make send-test-prompt   send one test prompt to the running daemon"
	@echo ""
	@echo "Release:"
	@echo "  make release            cut a release: minor bump (or VERSION=X.Y.Z), tag + push, kicks off CI/CD"
	@echo "  make release-dry        build UNSIGNED installers as CI artifacts (no tag/release; needs gh)"
	@echo ""
	@echo "Test Mac (Scaleway Apple silicon — for testing the installer; needs \`scw init\`):"
	@echo "  make scaleway-up        provision/reuse a cloud macOS host, print how to connect (24h MIN billing!)"
	@echo "  make scaleway-down      delete it (YES=1 skips confirm; still bills the 24h minimum)"
	@echo "  make scaleway-status    show the current Mac + connection details"
	@echo ""
	@echo "Vars: DEST=$(DEST)  SIDECAR_VENV=$(SIDECAR_VENV)  PYTHON=$(PYTHON)  SINK_PORT=$(SINK_PORT)"
	@echo "      VERSION=<X.Y.Z> (release, optional)  YES=1 (release/scaleway-down, skip confirm)"
	@echo "      SCALEWAY_ZONE=$(SCALEWAY_ZONE)  SCALEWAY_UP_TIMEOUT=$(SCALEWAY_UP_TIMEOUT) (seconds)"

.PHONY: build-binaries
build-binaries:
	@mkdir -p "$(DEST)"
	go build -o "$(DEST)/keld" ./cmd/keld
	go build -o "$(DEST)/keld-agent" ./cmd/keld-agent
	@echo "built keld + keld-agent -> $(DEST)"

# Create the sidecar venv (torch + gliner2) and a `keld-agent-sidecar` wrapper that
# sidecarBinPath() resolves (a regular executable file beside keld-agent). Avoids a
# full PyInstaller freeze for local dev; the daemon spawns it as `keld-agent-sidecar --port N`.
.PHONY: sidecar
sidecar:
	@command -v "$(PYTHON)" >/dev/null || { \
	  echo "ERROR: $(PYTHON) not found. The sidecar needs Python 3.12 (the host default 3.14 has no"; \
	  echo "       torch/gliner2 wheels). Install 3.12 (pyenv/apt/uv) or pass PYTHON=/path/to/python3.12."; \
	  exit 1; }
	"$(PYTHON)" -m venv "$(SIDECAR_VENV)"
	"$(SIDECAR_VENV)/bin/pip" install -q --upgrade pip
	"$(SIDECAR_VENV)/bin/pip" install -q -r sidecar/requirements.txt
	@printf '#!/bin/sh\nexec "%s/bin/python" "%s/sidecar/serve.py" "$$@"\n' "$(SIDECAR_VENV)" "$(CURDIR)" > "$(DEST)/keld-agent-sidecar"
	@chmod +x "$(DEST)/keld-agent-sidecar"
	@echo "sidecar venv + wrapper -> $(DEST)/keld-agent-sidecar"

.PHONY: install-service
install-service:
	"$(DEST)/keld-agent" install
	@echo "systemd --user service installed."

.PHONY: install-linux
install-linux: build-binaries sidecar install-service
	@echo ""
	@echo "keld-agent installed WITH sidecar (deterministic works instantly; the first ML"
	@echo "enrichment provisions the model ~1.9GB into ~/.keld/models, then the sidecar takes over)."
	@echo "If not yet configured, run:  keld login && keld signal setup"
	@echo "Visualize enrichments:      make enrichments-sink   (see README / the notes printed by this session)"

.PHONY: uninstall-linux
uninstall-linux:
	-"$(DEST)/keld-agent" uninstall
	@echo "service removed. To fully clean: rm -rf \"$(SIDECAR_VENV)\" \"$(DEST)\"/keld \"$(DEST)\"/keld-agent \"$(DEST)\"/keld-agent-sidecar"

.PHONY: enrichments-sink
enrichments-sink:
	python3 scripts/enrichments-sink.py $(SINK_PORT)

.PHONY: send-test-prompt
send-test-prompt:
	python3 scripts/send-test-prompt.py

# Cut a release: minor bump by default, or `make release VERSION=1.2.3`. Pushes a
# vX.Y.Z tag, which kicks off GoReleaser + the installers build. YES=1 skips the
# confirmation prompt.
.PHONY: release
release:
	@bash scripts/cut-release.sh $(if $(YES),-y,) $(VERSION)

# Dry run: trigger the installers workflow (unsigned installers on all 3 OSes as
# downloadable CI artifacts) without cutting a real release. Requires the gh CLI.
.PHONY: release-dry
release-dry:
	@command -v gh >/dev/null || { echo "release-dry needs the gh CLI (https://cli.github.com)"; exit 1; }
	gh workflow run installers.yml
	@echo "Triggered installers.yml — watch: gh run watch   (or the Actions tab)"

# ── Scaleway Apple silicon test Mac ─────────────────────────────────────────
# Spin up a real (Apple-hardware) cloud macOS host to test the signed .pkg/.dmg
# installer. There is no local macOS on Linux, and VirtualBox can only run *Intel*
# macOS while we ship *arm64* — so a cloud Mac is the only faithful test. Wraps `scw`
# via scripts/scaleway-mac.py (stdlib-only).
#
# ONE-TIME PREREQUISITES:
#   * scw CLI installed + authenticated:  scw init   (https://github.com/scaleway/scaleway-cli)
#   * Register your SSH *public* key in the Scaleway project BEFORE `scaleway-up`, or
#     SSH won't work on the Mac (VNC still does).
#   * Apple silicon quota > 0 for some type. New orgs start at 0 (anti-fraud gate); M1-M
#     is usually the only pre-cleared type. For M2/M4 types, request an increase in the
#     console → Quotas. `scaleway-up` auto-skips any type that reports quota 0/0.
#
# BILLING: 24-HOUR MINIMUM per Apple's licensing — `scaleway-down` before 24h have elapsed
#          STILL bills the full 24h. Rates ~€0.11/hr (M1-M) … ~€0.29/hr (M4-M).
# CONNECT: `scaleway-up`/`scaleway-status` print the IP + VNC URL. The VNC username/password
#          and the exact SSH command are on the server's Overview page in the console
#          (the API doesn't return the VNC password). Use any VNC client (e.g. Remmina).
#          DO NOT enable FileVault on the Mac — it locks out all remote (VNC/SSH) access.
SCALEWAY_ZONE ?= fr-par-1
SCALEWAY_UP_TIMEOUT ?= 1800   # seconds to keep polling for stock before giving up

# Reuse an existing Mac if there is one; else poll every type cheapest-first and create
# the first that is in stock AND your quota allows, then print how to connect.
.PHONY: scaleway-up
scaleway-up:
	SCALEWAY_ZONE=$(SCALEWAY_ZONE) SCALEWAY_UP_TIMEOUT=$(SCALEWAY_UP_TIMEOUT) python3 scripts/scaleway-mac.py up

# Delete the Mac(s) in the project. Interactive confirm unless YES=1. Remember the 24h min.
.PHONY: scaleway-down
scaleway-down:
	SCALEWAY_ZONE=$(SCALEWAY_ZONE) YES=$(YES) python3 scripts/scaleway-mac.py down

# Show the current Mac(s) + connection details.
.PHONY: scaleway-status
scaleway-status:
	SCALEWAY_ZONE=$(SCALEWAY_ZONE) python3 scripts/scaleway-mac.py status

.PHONY: freeze-check
freeze-check: ## run the PLAIN freeze + worker-spawn acceptance gate locally (Linux)
	KELD_OBFUSCATE=0 bash scripts/freeze-check-local.sh

.PHONY: obfuscate-check
obfuscate-check: ## run the OBFUSCATED freeze + worker-spawn acceptance gate locally (Linux)
	KELD_OBFUSCATE=1 bash scripts/freeze-check-local.sh
