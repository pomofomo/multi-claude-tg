.PHONY: build build-all test tidy clean install-deps lint

GO ?= go

build:
	$(GO) build -o bin/trd ./cmd/trd

build-all:
	bash scripts/build-binaries.sh

test:
	$(GO) test ./...

tidy:
	$(GO) mod tidy

lint:
	$(GO) vet ./...

clean:
	rm -f bin/trd bin/trd-*

install-deps:
	bash scripts/install.sh
