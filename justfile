set shell := ["bash", "-uc"]

default: check

fmt:
    go fmt ./...

test:
    go test ./...

test-full:
    ATOMIC_FULL_TESTS=1 go test -count=1 -timeout=20m ./internal/backup

race:
    go test -race ./...

check:
    go test ./...
    go vet ./...

build:
    mkdir -p build
    go build -trimpath -o build/atomic ./cmd/atomic

cross:
    mkdir -p build/cross
    GOOS=darwin GOARCH=arm64 go build -trimpath -o build/cross/atomic-darwin-arm64 ./cmd/atomic
    GOOS=linux GOARCH=amd64 go build -trimpath -o build/cross/atomic-linux-amd64 ./cmd/atomic
    GOOS=windows GOARCH=amd64 go build -trimpath -o build/cross/atomic-windows-amd64.exe ./cmd/atomic

run *args:
    go run ./cmd/atomic -- {{args}}
