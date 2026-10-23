STATICCHECK := go tool staticcheck

.PHONY: check fmt-check vet lint test test-bulk build install e2e panel

check: fmt-check vet lint test

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...
	go vet -tags devdrive ./tools/... ./cmd/...

lint:
	$(STATICCHECK) ./...
	$(STATICCHECK) -tags devdrive ./tools/...

test:
	go test -race ./...

test-bulk:
	VAULT_BULK_TESTS=1 go test -count=1 -run Thousands -v ./internal/syncer

build:
	CGO_ENABLED=0 go build -o bin/ ./cmd/...

install: build
	install -Dm755 bin/vaultd $(HOME)/.local/bin/vaultd
	install -Dm755 bin/vault $(HOME)/.local/bin/vault
	install -Dm644 packaging/systemd/vaultd.service $(HOME)/.config/systemd/user/vaultd.service
	systemctl --user daemon-reload
	@echo "Start it with: systemctl --user enable --now vaultd"

e2e:
	go test -tags e2e,devdrive ./test/e2e/...

panel:
	cd panel/frontend && npm ci && npm run build
	go build -tags desktop,production,webkit2_41 -o bin/vault-panel ./panel
