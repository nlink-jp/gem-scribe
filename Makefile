BINARY  := gem-scribe
DIST    := dist
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-s -w -X github.com/nlink-jp/gem-scribe/cmd.Version=$(VERSION)"

# macOS Developer ID signing / notarization (see nlink-jp/.github
# CONVENTIONS.md §Code Signing). Builds without these fall back to
# ad-hoc / un-notarized with a one-line warning.
CODESIGN_IDENTITY ?= Developer ID Application
NOTARY_PROFILE    ?= nlink-jp-notary

# darwin ships arm64 only (no amd64, no universal). linux/windows keep their matrix.
PLATFORMS := darwin/arm64 linux/amd64 linux/arm64 windows/amd64

.PHONY: build build-all package verify-release test vet lint check clean

## build: single binary for this machine → dist/
build:
	@mkdir -p $(DIST)
	go build $(LDFLAGS) -o $(DIST)/$(BINARY) .
	@scripts/codesign-darwin.sh $(DIST)/$(BINARY) "$(CODESIGN_IDENTITY)"

build-all:
	@mkdir -p $(DIST)
	@for p in $(PLATFORMS); do os=$${p%/*}; arch=$${p#*/}; \
		ext=""; [ "$$os" = windows ] && ext=".exe"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(LDFLAGS) -o $(DIST)/$(BINARY)-$$os-$$arch$$ext . ; \
	done
	@scripts/codesign-darwin.sh $(DIST)/$(BINARY)-darwin-arm64 "$(CODESIGN_IDENTITY)" "$(BINARY)"

## package: build all platforms, archive with the canonical binary name inside,
## and notarize the darwin build.
package: build-all
	@cd $(DIST) && for p in $(PLATFORMS); do os=$${p%/*}; arch=$${p#*/}; \
		ext=""; [ "$$os" = windows ] && ext=".exe"; \
		stage=_pkg; rm -rf $$stage; mkdir -p $$stage; \
		cp "$(BINARY)-$$os-$$arch$$ext" "$$stage/$(BINARY)$$ext"; \
		cp ../README.md ../LICENSE $$stage/; \
		base="$(BINARY)-$(VERSION)-$$os-$$arch"; \
		if [ "$$os" = linux ]; then ( cd $$stage && tar -czf "../$$base.tar.gz" * ); \
		else ( cd $$stage && zip -q "../$$base.zip" * ); fi; \
		rm -rf $$stage; \
	done
	@scripts/notarize-darwin.sh $(DIST)/$(BINARY)-$(VERSION)-darwin-arm64.zip "$(NOTARY_PROFILE)"

## verify-release: refuse to release an un-notarized zip (marker gate)
verify-release:
	@test -f "$(DIST)/$(BINARY)-$(VERSION)-darwin-arm64.zip.notarized" || { \
		echo "verify-release: FAIL — $(BINARY)-$(VERSION)-darwin-arm64.zip has no notarization marker."; \
		echo "  make package must end with '[notarize] ...: Accepted'. Do not upload this zip."; \
		exit 1; }
	@test "$(DIST)/$(BINARY)-$(VERSION)-darwin-arm64.zip.notarized" -nt "$(DIST)/$(BINARY)-$(VERSION)-darwin-arm64.zip" || { \
		echo "verify-release: FAIL — the zip was rebuilt after its marker (re-run make package)."; \
		exit 1; }
	@tmp=$$(mktemp -d) && \
		unzip -oq "$(DIST)/$(BINARY)-$(VERSION)-darwin-arm64.zip" -d "$$tmp" && \
		"$$tmp/$(BINARY)" --version && \
		spctl -a -vv -t install "$$tmp/$(BINARY)" 2>&1 | head -2 || true; \
		rm -rf "$$tmp"
	@echo "verify-release: OK ($(VERSION), notarization marker present)"

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

check: vet test build

clean:
	rm -rf $(DIST)

# Homebrew tap generation. After `make package`, `make brew` generates the
# formula from the built darwin-arm64 zip into the local homebrew-tap checkout.
BREW_KIND := formula
BREW_DESC := Cloud speech-to-text CLI and MCP server on Vertex AI Gemini
include scripts/release-brew.mk
