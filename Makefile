.PHONY: ci test vet fmt build run-once dry-run conformance

ci: fmt vet test

fmt:
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "gofmt: files need formatting" && exit 1)

test:
	go test ./... -race -count=1

vet:
	go vet ./...

build:
	go build -o bin/directorysyncd ./cmd/directorysyncd

# Check a live directory against the contract, one line per acceptance
# criterion. Read-only unless you pass -write-id.
#   DIRECTORY_BASE_URL=... DIRECTORY_TOKEN=... make conformance
conformance:
	go run ./cmd/conformance -base-url "$$DIRECTORY_BASE_URL"

# Report what one cycle WOULD do, writing nothing anywhere. Always start here:
#   DIRECTORY_BASE_URL=... DIRECTORY_TOKEN=... make dry-run
dry-run:
	go run ./cmd/directorysyncd -once -dry-run

# Single full reconciliation. Requires a real store.Store; the worker refuses
# to run against a real directory with the in-memory one.
run-once:
	go run ./cmd/directorysyncd -once
