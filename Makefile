BINARY_NAME=deepseek
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS=-ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build
build:
	go build -o $(BINARY_NAME) $(LDFLAGS) ./cmd/deepseek

.PHONY: test
test:
	go test ./...

.PHONY: test-cover
test-cover:
	go test ./... -coverprofile=cover.out -covermode=atomic
	go tool cover -func=cover.out | tail -1

# The README carries a coverage badge. This is what keeps it from
# quietly becoming a lie: CI fails if coverage drops below the floor.
# Raise COVERAGE_FLOOR (and the badge) when coverage improves.
COVERAGE_FLOOR=68
.PHONY: cover-gate
cover-gate:
	@go test ./... -coverprofile=cover.out -covermode=atomic >/dev/null
	@total=$$(go tool cover -func=cover.out | awk '/^total:/ {gsub(/%/,"",$$3); print $$3}'); \
	 echo "coverage: $$total% (floor $(COVERAGE_FLOOR)%)"; \
	 awk -v t=$$total -v f=$(COVERAGE_FLOOR) 'BEGIN { if (t+0 < f+0) { print "FAIL: coverage below floor"; exit 1 } }'

# The documentation corpus compiled into the binary by `deepseek docs`.
#
# Source of truth is the mirror repo, which converts api-docs.deepseek.com
# page by page and extracts the FAQ out of its JS bundle. A sibling
# checkout is used when there is one — that is the loop while working on
# both — and otherwise it is fetched, so CI and a bare clone both work.
CORPUS=internal/docs/corpus.tar.gz
DOCS_REPO=https://github.com/thevibeworks/deepseek-docs

.PHONY: corpus
corpus:
	@if [ -d ../deepseek-docs/content/en ]; then \
	  echo "packing from ../deepseek-docs"; \
	  tar -C ../deepseek-docs/content -czf $(CORPUS) en; \
	else \
	  echo "fetching from $(DOCS_REPO)"; \
	  tmp=$$(mktemp -d); \
	  curl -sSL $(DOCS_REPO)/archive/refs/heads/main.tar.gz | tar -xz -C $$tmp --strip-components=1; \
	  tar -C $$tmp/content -czf $(CORPUS) en; \
	  rm -rf $$tmp; \
	fi
	@echo "$(CORPUS): $$(du -h $(CORPUS) | cut -f1), $$(tar tzf $(CORPUS) | grep -c '\.md$$') pages"

# Fails if the embedded corpus cannot be read back. An unreadable corpus
# breaks `docs` on a released binary and nothing else would catch it.
.PHONY: corpus-check
corpus-check:
	@go test ./internal/docs/ -run TestEmbeddedCorpusLoads -count=1

# The free-tier gateway. A separate Go module on purpose: the CLI's
# dependency list is part of its pitch, and a server has no business
# widening it. See gateway/DESIGN.md.
GATEWAY_BINARY=dsgate

.PHONY: gateway
gateway:
	cd gateway && go build -o ../$(GATEWAY_BINARY) -ldflags "-s -w -X main.version=$(VERSION)" ./cmd/dsgate

# Needs the CLI binary: the interop test runs the real `deepseek` against
# a real gateway, which is the only thing that proves the two independent
# implementations of the enrolment protocol actually agree.
.PHONY: gateway-test
gateway-test: build
	cd gateway && go test ./... -race -count=1

.PHONY: gateway-check
gateway-check: gateway-test
	@test -z "$$(cd gateway && gofmt -l .)" || { echo "gateway needs gofmt:"; cd gateway && gofmt -l .; exit 1; }
	cd gateway && go vet ./...
	$(MAKE) gateway

# The rate card exists twice — internal/deepseek/pricing.go for the CLI's
# cost estimates, gateway/internal/meter/meter.go for the gateway's
# budget — because they are separate modules and neither can import the
# other. Drift would mean the gateway believed it had spent a different
# amount than it had. This is what catches that.
.PHONY: price-check
price-check:
	@pattern='CacheHitInput: [0-9.]+, CacheMissInput: [0-9.]+, Output: [0-9.]+'; \
	 cli=$$(grep -oE "$$pattern" internal/deepseek/pricing.go | sort); \
	 gw=$$(grep -oE "$$pattern" gateway/internal/meter/meter.go | sort); \
	 if [ -z "$$cli" ] || [ -z "$$gw" ]; then \
	   echo "FAIL: could not find a rate card in one of the two files"; exit 1; \
	 fi; \
	 if [ "$$cli" != "$$gw" ]; then \
	   echo "FAIL: the CLI and gateway rate cards have drifted"; \
	   echo "  cli:     $$cli"; echo "  gateway: $$gw"; exit 1; \
	 fi; \
	 echo "rate cards match:"; echo "$$cli" | sed 's/^/  /'

# The site is generated HTML plus one page that is an application. The
# generator has its own --check; these cover the parts it cannot see —
# that the playground's script and markup still agree, that all three
# implementations of the enrolment puzzle still produce the same answers,
# that the sea still runs, stops when it should, and draws the same whale
# on the canvas as whale.svg draws for readers without JavaScript, and
# that the design rules in DESIGN.md still describe the stylesheet.
.PHONY: site-check
site-check:
	python3 site/build.py --check
	node site/pow.test.js
	node site/md.test.js
	node site/playground.test.js
	node site/playground.dom.test.js
	node site/pricing.test.js
	node site/waves.test.js
	node site/waves.dom.test.js
	./site/bans.sh

.PHONY: site
site:
	python3 site/build.py

.PHONY: vet
vet:
	go vet ./...

.PHONY: fmt
fmt:
	gofmt -w .

.PHONY: fmt-check
fmt-check:
	@test -z "$$(gofmt -l .)" || { echo "needs gofmt:"; gofmt -l .; exit 1; }

.PHONY: check
check: fmt-check vet test corpus-check price-check build gateway-check site-check
	@echo "All checks passed"

# Live smoke test against the real API. Needs DEEPSEEK_API_KEY and costs
# a fraction of a cent; kept out of `check` so the default path never
# depends on the network or on someone's balance.
.PHONY: smoke
smoke: build
	./$(BINARY_NAME) check

# Aliases: the binary answers to whichever name invoked it, so these are
# symlinks rather than copies — one binary, three ways to type it.
ALIASES=ds dscli

.PHONY: install
install: build
	@bindir=$${GOPATH:-$$HOME/go}/bin; mkdir -p $$bindir; \
	 cp $(BINARY_NAME) $$bindir/$(BINARY_NAME); \
	 for a in $(ALIASES); do ln -sf $(BINARY_NAME) $$bindir/$$a; done; \
	 echo "Installed $(BINARY_NAME) (aliases: $(ALIASES)) in $$bindir"

.PHONY: completions
completions: build
	@mkdir -p completions
	./$(BINARY_NAME) completion bash > completions/$(BINARY_NAME).bash
	./$(BINARY_NAME) completion zsh  > completions/_$(BINARY_NAME)
	./$(BINARY_NAME) completion fish > completions/$(BINARY_NAME).fish

.PHONY: clean
clean:
	go clean
	rm -f $(BINARY_NAME) cover.out
	rm -rf dist completions

.DEFAULT_GOAL := build
