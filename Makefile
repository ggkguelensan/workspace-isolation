# wi — deterministic multi-repo workspace isolation.
#
# This Makefile is a thin convenience wrapper over the standard Go toolchain; it
# encodes the exact per-commit gate the project builds under (build, test, gofmt,
# and a Linux cross-vet so the macOS-developed tree stays portable). It adds NO
# build-time dependency — every target is plain `go ...` / `gofmt`.

# The single binary and its package.
BINARY := wi
CMD    := ./cmd/wi

# Default target: the full gate, the same checks a commit must pass.
.PHONY: all
all: fmt-check vet vet-linux test

# Compile every package (no binary emitted).
.PHONY: build
build:
	go build ./...

# Build the wi binary into ./bin/wi for local use.
.PHONY: bin
bin:
	go build -o bin/$(BINARY) $(CMD)

# Install wi into $GOBIN (or $GOPATH/bin) as `wi`.
.PHONY: install
install:
	go install $(CMD)

# Run the whole test suite (fitness guards included).
.PHONY: test
test:
	go test ./...

# Run the suite with the race detector — slower, for pre-publish confidence.
.PHONY: test-race
test-race:
	go test -race ./...

# Format every Go source in place.
.PHONY: fmt
fmt:
	gofmt -w .

# Fail if any Go source is not gofmt-clean (the commit gate; prints offenders).
.PHONY: fmt-check
fmt-check:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; echo "$$unformatted"; exit 1; \
	fi

# Vet on the host platform.
.PHONY: vet
vet:
	go vet ./...

# Vet for linux/amd64 — the project develops on darwin but must stay portable,
# so platform-guarded files (locks, host probes) are checked under both GOOSes.
.PHONY: vet-linux
vet-linux:
	GOOS=linux GOARCH=amd64 go vet ./...

# Remove local build artifacts (the toolchain cache is left untouched).
.PHONY: clean
clean:
	rm -rf bin
