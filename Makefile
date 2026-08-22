.PHONY: build build-windows build-windows-amd64 fmt lint test test-race cover check

build:
	go build ./cmd/saveknot

build-windows: build-windows-amd64

build-windows-amd64:
	mkdir -p dist
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o dist/saveknot-windows-amd64.exe ./cmd/saveknot

fmt:
	golangci-lint fmt

lint:
	golangci-lint run ./...

test:
	go test ./...

test-race:
	go test -race ./...

cover:
	go test -cover ./...

check: lint test test-race
	git diff --check
