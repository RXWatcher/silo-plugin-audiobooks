BINARY := silo-plugin-audiobooks
GO ?= go
PNPM ?= pnpm

.PHONY: build test test-go test-web clean
build:
	cd web && $(PNPM) install --frozen-lockfile && $(PNPM) run build
	$(GO) build -o $(BINARY) ./cmd/silo-plugin-audiobooks

test: test-go test-web

test-go:
	$(GO) test ./...

test-web:
	cd web && $(PNPM) run test --run
clean:
	rm -f $(BINARY)
	rm -rf web/dist/
