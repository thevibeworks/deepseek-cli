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
	go test ./... -coverprofile=cover.out
	go tool cover -func=cover.out | tail -1

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
check: fmt-check vet test build
	@echo "All checks passed"

# Live smoke test against the real API. Needs DEEPSEEK_API_KEY and costs
# a fraction of a cent; kept out of `check` so the default path never
# depends on the network or on someone's balance.
.PHONY: smoke
smoke: build
	./$(BINARY_NAME) check

.PHONY: install
install: build
	@mkdir -p $${GOPATH:-$$HOME/go}/bin
	@cp $(BINARY_NAME) $${GOPATH:-$$HOME/go}/bin/$(BINARY_NAME)
	@echo "Installed as '$(BINARY_NAME)'"

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
