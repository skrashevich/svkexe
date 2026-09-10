.PHONY: build gateway agent test-agent run test clean

BINARY_NAME=gateway
BUILD_DIR=./bin
CMD_DIR=./cmd/gateway
VERSION_PKG=github.com/skrashevich/svkexe/internal/version

# Build metadata stamped into the binary via -ldflags -X. Fallbacks keep
# `make gateway` working outside a git checkout (e.g. a source tarball).
#
# safe.directory is declared explicitly because svkexe-update.service builds as
# root from a checkout that need not be root-owned. git would then refuse with
# "dubious ownership" and, since these fallbacks swallow the error, the binary
# would be stamped "dev"/"unknown" — leaving the dashboard with no version to
# show and nothing for the update check to compare against.
GIT := git -c safe.directory=$(CURDIR)
VERSION := $(shell $(GIT) describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT := $(shell $(GIT) rev-parse HEAD 2>/dev/null || echo unknown)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PICOCLAW_VERSION := $(shell sed -n 's/^PICOCLAW_VERSION=//p' agent/upstream.env 2>/dev/null || echo unknown)
SHELLEY_COMMIT := $(shell sed -n 's/^SHELLEY_COMMIT=//p' agent/upstream.env 2>/dev/null || echo unknown)
PICOCLAW_VERSION := $(if $(PICOCLAW_VERSION),$(PICOCLAW_VERSION),unknown)
SHELLEY_COMMIT := $(if $(SHELLEY_COMMIT),$(SHELLEY_COMMIT),unknown)

LDFLAGS := -s -w \
	-X $(VERSION_PKG).Version=$(VERSION) \
	-X $(VERSION_PKG).Commit=$(COMMIT) \
	-X $(VERSION_PKG).BuildDate=$(BUILD_DATE) \
	-X $(VERSION_PKG).PicoClawVersion=$(PICOCLAW_VERSION) \
	-X $(VERSION_PKG).ShelleyCommit=$(SHELLEY_COMMIT)

build: gateway agent

gateway:
	mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -trimpath -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)

run: build
	$(BUILD_DIR)/$(BINARY_NAME)

test:
	go test ./...

clean:
	rm -rf $(BUILD_DIR)
	go clean

agent:
	./scripts/build-agent.sh build

test-agent:
	./scripts/build-agent.sh test
