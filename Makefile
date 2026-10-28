# Local parity for the CI gates: `make ci` runs the lint, test, and race
# lane commands from .github/workflows/ci.yml. CI stays authoritative;
# this target is the fast local floor before push.
#
# The coverage floor is a percentage of statements and may only rise as
# tests land. The finish line is 90%.

GOLANGCI_VERSION ?= 2.12.2
ACTIONLINT_VERSION ?= 1.7.12
COVERAGE_FLOOR ?= 87.1

GOLANGCI := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$(GOLANGCI_VERSION)
ACTIONLINT := go run github.com/rhysd/actionlint/cmd/actionlint@v$(ACTIONLINT_VERSION)

.PHONY: ci lint test race

ci: lint test race

lint:
	go vet ./...
	$(GOLANGCI) run ./...
	$(GOLANGCI) fmt --diff ./...
	$(ACTIONLINT)
	go mod tidy -diff
	@over=$$(git ls-files '*.go' | while IFS= read -r f; do \
		n=$$(wc -l < "$$f"); \
		if [ "$$n" -gt 500 ]; then printf '%s (%s lines)\n' "$$f" "$$n"; fi; \
	done); \
	if [ -n "$$over" ]; then \
		echo "Files over the 500-line cap:"; echo "$$over"; exit 1; \
	fi; \
	echo "500-line cap: OK ($$(git ls-files '*.go' | wc -l) files checked)"

test:
	go build ./...
	go test ./... -count=1

race:
	go test ./... -race -count=1 -covermode=atomic -coverprofile=coverage.out
	@total=$$(go tool cover -func=coverage.out | awk '/^total:/ { gsub(/%/, "", $$3); print $$3 }'); \
	echo "total coverage: $${total}% (floor $(COVERAGE_FLOOR)%)"; \
	awk -v total="$$total" -v floor="$(COVERAGE_FLOOR)" 'BEGIN { if (total + 0 < floor + 0) { printf "coverage %.1f%% is below floor %s%%\n", total, floor; exit 1 } }'
