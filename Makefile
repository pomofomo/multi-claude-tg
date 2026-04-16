.PHONY: build build-all install restart test tidy clean install-deps lint

GO ?= go

build:
	$(GO) build -o bin/trd ./cmd/trd

install: build
	mkdir -p ~/.local/bin
	rm -f ~/.local/bin/trd
	cp bin/trd ~/.local/bin/trd

restart: install
	@echo "Restarting trd dispatcher in tmux session 'trd'..."
	tmux send-keys -t trd C-c 2>/dev/null || true
	sleep 1
	tmux send-keys -t trd 'trd start' Enter

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
