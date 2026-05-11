BINARY := continuum-plugin-audiobooks
GO ?= go

.PHONY: build test clean
build:
	cd web && pnpm install --frozen-lockfile && pnpm run build && cd ..
	$(GO) build -o $(BINARY) ./cmd/continuum-plugin-audiobooks
test:
	$(GO) test ./...
	cd web && pnpm run test --run
clean:
	rm -f $(BINARY)
	rm -rf web/dist/
