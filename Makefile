# WhiteTransport — Test & Build Targets
# Usage: make <target>

.PHONY: all test test-unit test-integration test-ts test-smoke test-e2e \
        vet build clean help

REPO_ROOT := $(shell pwd)
GO_DIR := $(REPO_ROOT)/core/go
TS_DIR := $(REPO_ROOT)/packages/any-transport
SMOKE_SCRIPT := $(REPO_ROOT)/tests/e2e/smoke/smoke-e2e-test.sh
APPIUM_DIR := $(REPO_ROOT)/tests/e2e/ui-appium

# ── Default ──────────────────────────────────────────────────────────
all: test

# ── Fast unit tests (no credentials, no network) ─────────────────────
test-unit:
	@echo "=== Go unit tests ==="
	cd $(GO_DIR) && go test -short -count=1 ./...
	@echo ""
	@echo "=== TS unit tests ==="
	cd $(TS_DIR) && npm test

# ── Integration tests (require WT_INTEGRATION=1 + credentials) ───────
test-integration:
	cd $(GO_DIR) && WT_INTEGRATION=1 go test -tags=integration -count=1 -timeout 120s ./tests/integration/...
	cd $(GO_DIR) && WT_INTEGRATION=1 go test -tags=integration -count=1 -timeout 180s ./tests/

# ── Real WBStream tests (require WB_NODE/CLIENT_ACCESS_TOKEN) ────────
test-wbstream-real:
	cd $(GO_DIR) && go test -tags=integration -count=1 -timeout 120s -run TestRealWB ./tests/integration/

# ── TypeScript unit tests only ───────────────────────────────────────
test-ts:
	cd $(TS_DIR) && npm test

# ── Smoke E2E (build + config validate + vet) ────────────────────────
test-smoke:
	bash $(SMOKE_SCRIPT)

# ── Smoke E2E with daemon startup ────────────────────────────────────
test-smoke-daemon:
	WT_SMOKE_DAEMON=1 bash $(SMOKE_SCRIPT)

# ── UI E2E via Appium (requires device/emulator) ─────────────────────
test-e2e:
	@if [ ! -d "$(APPIUM_DIR)/node_modules" ]; then \
		echo "Install deps first: cd $(APPIUM_DIR) && npm install"; \
		exit 1; \
	fi
	cd $(APPIUM_DIR) && npm test

# ── Run all fast checks (unit + vet + TS) ───────────────────────────
test: vet test-unit

# ── Go vet ───────────────────────────────────────────────────────────
vet:
	cd $(GO_DIR) && go vet ./...

# ── Build daemon binary ──────────────────────────────────────────────
build:
	cd $(GO_DIR) && go build -o whitetransportd ./cmd/whitetransportd/

# ── Clean build artifacts ────────────────────────────────────────────
clean:
	rm -f $(GO_DIR)/whitetransportd
	rm -f /tmp/whitetransportd-smoke-test

# ── Help ─────────────────────────────────────────────────────────────
help:
	@echo "WhiteTransport test targets:"
	@echo ""
	@echo "  make test              Run vet + unit tests (fast, default)"
	@echo "  make test-unit         Go + TS unit tests only"
	@echo "  make test-ts           TypeScript unit tests only"
	@echo "  make test-integration  Integration tests (needs WT_INTEGRATION=1 + secrets)"
	@echo "  make test-wbstream-real Real WBStream DataChannel tests (needs WB tokens)"
	@echo "  make test-smoke        Smoke E2E: build + config validate + vet"
	@echo "  make test-smoke-daemon Smoke E2E + daemon startup + health check"
	@echo "  make test-e2e          UI E2E via Appium (needs device/emulator)"
	@echo "  make vet               Go vet only"
	@echo "  make build             Build whitetransportd binary"
	@echo "  make clean             Remove build artifacts"
