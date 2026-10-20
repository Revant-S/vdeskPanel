STATICCHECK := go run honnef.co/go/tools/cmd/staticcheck@v0.8.2

.PHONY: check fmt-check vet lint test build e2e panel

check: fmt-check vet lint test

fmt-check:
	@out=$$(gofmt -l $$(git ls-files '*.go')); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

lint:
	$(STATICCHECK) ./...

test:
	go test -race ./...

build:
	CGO_ENABLED=0 go build -o bin/ ./cmd/...

e2e:
	go test -tags e2e,devdrive ./test/e2e/...

panel:
	cd panel/frontend && npm ci && npm run build
	go build -tags desktop,production,webkit2_41 -o bin/vault-panel ./panel
