# Release binaries are built reproducibly: pinned Go toolchain, no cgo,
# -trimpath, no build IDs or VCS stamps. Anyone can run `make verify-pins` to
# rebuild them from source and check them against the hashes pinned in the
# launcher (skills/tts/scripts/tts).

SRC        := skills/tts/src
LAUNCHER   := skills/tts/scripts/tts
VERSION    := $(shell sed -n 's/^VERSION=//p' $(LAUNCHER))
GO_VERSION := $(shell sed -n 's/^toolchain go//p' $(SRC)/go.mod)
TARGETS    := darwin-arm64 darwin-amd64 linux-arm64 linux-amd64
LDFLAGS    := -s -w -buildid= -X main.version=$(VERSION)
GOBUILD    := GOTOOLCHAIN=go$(GO_VERSION) CGO_ENABLED=0 go -C $(SRC) build -trimpath -buildvcs=false -mod=readonly -ldflags '$(LDFLAGS)'

.PHONY: build test lint dist checksums pin verify-pins clean

build: ## build for this machine into dist/tts
	$(GOBUILD) -o $(CURDIR)/dist/tts .

test:
	go -C $(SRC) vet ./...
	go -C $(SRC) test -race -count=1 ./...

lint:
	shellcheck $(LAUNCHER)
	test -z "$$(gofmt -l $(SRC))" || (gofmt -l $(SRC); exit 1)

dist: $(TARGETS:%=dist/tts-%)

dist/tts-%: FORCE
	@mkdir -p dist
	GOOS=$(word 1,$(subst -, ,$*)) GOARCH=$(word 2,$(subst -, ,$*)) $(GOBUILD) -o $(CURDIR)/$@ .

checksums: dist
	(cd $(CURDIR)/dist && shasum -a 256 $(TARGETS:%=tts-%) > SHA256SUMS) && cat dist/SHA256SUMS

# Write the hashes of freshly built binaries into the launcher. Run before
# tagging a release, then commit.
pin: checksums
	@for t in $(TARGETS); do \
		h=$$(awk -v f="tts-$$t" '$$2 == f { print $$1 }' dist/SHA256SUMS); \
		v=$$(echo $$t | tr - _); \
		perl -pi -e "s/^SHA256_$$v=.*/SHA256_$$v=$$h/" $(LAUNCHER); \
	done
	@grep '^SHA256_' $(LAUNCHER)

# Rebuild from source and fail unless every binary matches the launcher's pins.
verify-pins: checksums
	@fail=0; for t in $(TARGETS); do \
		v=$$(echo $$t | tr - _); \
		want=$$(sed -n "s/^SHA256_$$v=//p" $(LAUNCHER)); \
		got=$$(awk -v f="tts-$$t" '$$2 == f { print $$1 }' dist/SHA256SUMS); \
		if [ "$$want" = "$$got" ]; then echo "ok        tts-$$t $$got"; \
		else echo "MISMATCH  tts-$$t pinned=$$want built=$$got"; fail=1; fi; \
	done; exit $$fail

clean:
	rm -rf dist

FORCE:
