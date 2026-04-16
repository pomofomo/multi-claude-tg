.PHONY: build build-all install test tidy clean install-deps lint

GO ?= go

build:
	$(GO) build -o bin/trd ./cmd/trd

install: build
	mkdir -p ~/.local/bin
	rm -f ~/.local/bin/trd
	cp bin/trd ~/.local/bin/trd

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
