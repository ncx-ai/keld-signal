# The Linux leg of the conformance matrix, as a container.
#
#   make conformance-linux TOOL=claude_code [CHAIN=A] [SEED=42]
#
# Ubuntu 24.04 because that is the closest thing to `ubuntu-latest` on GitHub,
# and because its default `python3` IS 3.12 — the interpreter the sidecar needs
# and the one `keld-conform check` falls back to when it reads refseries.db.
#
# ⚠️ **`sqlite3` is installed deliberately, not as a convenience.** The
# `store_rows` checkpoint shells out to `sqlite3` (or `python3`'s module) —
# `checkpoints/gather.go` returns "no sqlite reader on PATH" rather than zero
# rows when neither exists, so an image without it turns a real checkpoint into
# an error and the run reads as broken for the wrong reason.
#
# ⚠️ **SERVICE REGISTRATION IS NOT EXERCISED HERE AND CANNOT BE.** There is no
# launchd and no running systemd user bus in a container, so `keld-agent
# install` has nothing to register with; the harness already runs the daemon in
# the foreground for the same reason (see lib/isolate.sh's header). The
# container leg therefore proves the tool, the adapters, the lanes and the five
# checkpoints — not AC-12's installer path, which belongs to the VM legs and to
# the GitHub macOS/Windows runners. The entrypoint prints that line every run so
# a green container result is never mistaken for a green install result.
FROM ubuntu:24.04

# `stable` resolves the current toolchain from go.dev at build time rather than
# pinning a version this file would then have to chase; pass
# --build-arg GO_VERSION=go1.26.1 to freeze one.
ARG GO_VERSION=stable
ARG NODE_MAJOR=22

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    GOTOOLCHAIN=local \
    GOCACHE=/cache/go-build \
    GOMODCACHE=/cache/go-mod \
    PATH=/usr/local/go/bin:/usr/local/node/bin:$PATH

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git jq xz-utils tar gzip procps \
      build-essential pkg-config \
      python3 python3-venv python3-pip \
      sqlite3 \
    && rm -rf /var/lib/apt/lists/*

# Node from the official tarball rather than NodeSource: one fetch, no apt repo
# key to expire, and the major is an ARG so the matrix can move it.
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in amd64) narch=x64 ;; arm64) narch=arm64 ;; *) echo "unsupported arch $arch" >&2; exit 1 ;; esac; \
    ver="$(curl -fsSL https://nodejs.org/dist/index.json | jq -r --arg m "v${NODE_MAJOR}." '[.[] | select(.version | startswith($m))][0].version')"; \
    curl -fsSL "https://nodejs.org/dist/${ver}/node-${ver}-linux-${narch}.tar.xz" -o /tmp/node.tar.xz; \
    mkdir -p /usr/local/node; \
    tar -xJf /tmp/node.tar.xz -C /usr/local/node --strip-components=1; \
    rm /tmp/node.tar.xz; \
    node --version; npm --version

RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in amd64) garch=amd64 ;; arm64) garch=arm64 ;; *) echo "unsupported arch $arch" >&2; exit 1 ;; esac; \
    if [ "$GO_VERSION" = "stable" ]; then \
      gv="$(curl -fsSL 'https://go.dev/dl/?mode=json' | jq -r '[.[] | select(.stable)][0].version')"; \
    else gv="$GO_VERSION"; fi; \
    curl -fsSL "https://go.dev/dl/${gv}.linux-${garch}.tar.gz" -o /tmp/go.tar.gz; \
    tar -C /usr/local -xzf /tmp/go.tar.gz; \
    rm /tmp/go.tar.gz; \
    go version

# Where the frozen Linux sidecar lands when the entrypoint fetches one. The
# harness looks for it at $HOME/.local/bin/keld-agent-sidecar, the same
# well-known dir install.sh uses.
RUN mkdir -p /root/.local/bin /cache /work

WORKDIR /repo
ENTRYPOINT ["/repo/scripts/conformance/compose/entrypoint.sh"]
