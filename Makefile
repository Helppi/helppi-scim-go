.PHONY: test vet lint build run-once

test:
	go test ./... -race -count=1

vet:
	go vet ./...

build:
	go build -o bin/directorysyncd ./cmd/directorysyncd

# Single full reconciliation against a real directory:
#   DIRECTORY_BASE_URL=... DIRECTORY_TOKEN=... make run-once
run-once:
	go run ./cmd/directorysyncd -once
