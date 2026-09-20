.PHONY: build build-windows build-windows-amd64 ui-check fmt fmt-check lint mod-check test test-race cover check

ui-check:
	node --check internal/api/web/app.js
	node --test internal/api/web/*.test.mjs

build:
	go build ./cmd/saveknot

build-windows: build-windows-amd64

build-windows-amd64:
	mkdir -p dist
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -H=windowsgui" -o dist/saveknot-windows-amd64.exe ./cmd/saveknot

fmt:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.0 fmt

fmt-check:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.0 fmt --diff

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.0 run ./...

mod-check:
	go mod tidy -diff

test:
	go test ./...

test-race:
	go test -race ./...

cover:
	go test -cover ./...

check: ui-check fmt-check lint mod-check test test-race
	git diff --check
