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
	@echo "Vars: DEST=$(DEST)  SIDECAR_VENV=$(SIDECAR_VENV)  PYTHON=$(PYTHON)  SINK_PORT=$(SINK_PORT)"
	@echo "      VERSION=<X.Y.Z> (release, optional)  YES=1 (release, skip confirm)"

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
