set shell := ["bash", "-uc"]

default: check

fmt:
    go fmt ./...

test:
    go test ./...

race:
    go test -race ./...

check:
    go test ./...
    go vet ./...

build:
    mkdir -p build
    go build -trimpath -o build/ressik ./cmd/ressik

cross:
    mkdir -p build/cross
    GOOS=darwin GOARCH=arm64 go build -trimpath -o build/cross/ressik-darwin-arm64 ./cmd/ressik
    GOOS=linux GOARCH=amd64 go build -trimpath -o build/cross/ressik-linux-amd64 ./cmd/ressik
    GOOS=windows GOARCH=amd64 go build -trimpath -o build/cross/ressik-windows-amd64.exe ./cmd/ressik

run *args:
    go run ./cmd/ressik -- {{args}}
