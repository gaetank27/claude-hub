# Convenience targets. Go is not on PATH by default: `export PATH=$PATH:/usr/local/go/bin`.
#
# DOCS_BASE / DOCS_SECRET parameterize the documentation flow against the running
# instance. The live unit binds the LAN IP with OIDC on, so override them, e.g.:
#   make docs DOCS_BASE=http://192.0.2.10:8787 DOCS_SECRET="$$SYSTEM_SESSION_SECRET"

GO ?= go
DOCS_BASE ?= http://127.0.0.1:8787
DOCS_SECRET ?=
# The default deployment auto-generates a binary HMAC key at <DataDir>/session.key
# (SYSTEM_SESSION_SECRET unset). It can contain NUL bytes, so it must be read from
# the file, not passed as a string. docgen reads it via SYSTEM_SESSION_SECRET_FILE.
DOCS_SECRET_FILE ?= $(HOME)/.local/share/system/session.key
DOCS_EMAIL ?= admin@example.com
# UI language(s) to render the manual in: en | fr | all. Screenshots land in
# web/public/help/screenshots/<lang>/ and the PDF as dist/claude-hub-manual-<lang>.pdf.
DOCS_LANG ?= all

.PHONY: build-backend build-docgen build-frontend docs screenshots pdf

build-frontend:
	cd web && npm run build

build-backend:
	$(GO) build -o bin/backend ./cmd/backend
	$(GO) build -o bin/session-runner ./cmd/session-runner
	$(GO) build -o bin/rag-mcp ./cmd/rag-mcp
	$(GO) build -o bin/tools-mcp ./cmd/tools-mcp
	$(GO) build -o bin/ask-mcp ./cmd/ask-mcp

build-docgen:
	$(GO) build -o bin/docgen ./cmd/docgen

# screenshots → rebuild (embed PNGs) → pdf. The screenshots must be captured and
# embedded before the PDF is printed so the manual shows the fresh images.
docs: build-docgen screenshots build-frontend build-backend pdf
	@echo "Manuals ready: dist/claude-hub-manual-*.pdf (one per language)"
	@echo "Restart the backend to serve the freshly embedded screenshots."

screenshots: build-docgen
	SYSTEM_SESSION_SECRET="$(DOCS_SECRET)" SYSTEM_SESSION_SECRET_FILE="$(DOCS_SECRET_FILE)" ./bin/docgen -mode screenshots -lang "$(DOCS_LANG)" -base "$(DOCS_BASE)" -email "$(DOCS_EMAIL)"

pdf: build-docgen
	SYSTEM_SESSION_SECRET="$(DOCS_SECRET)" SYSTEM_SESSION_SECRET_FILE="$(DOCS_SECRET_FILE)" ./bin/docgen -mode pdf -lang "$(DOCS_LANG)" -base "$(DOCS_BASE)" -email "$(DOCS_EMAIL)"
