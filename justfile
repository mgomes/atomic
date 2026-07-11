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
    go build -trimpath -o build/rs ./cmd/rs

cross:
    mkdir -p build/cross
    GOOS=darwin GOARCH=arm64 go build -trimpath -o build/cross/rs-darwin-arm64 ./cmd/rs
    GOOS=linux GOARCH=amd64 go build -trimpath -o build/cross/rs-linux-amd64 ./cmd/rs
    GOOS=windows GOARCH=amd64 go build -trimpath -o build/cross/rs-windows-amd64.exe ./cmd/rs

run *args:
    go run ./cmd/rs -- {{args}}
