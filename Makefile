.PHONY: build build-all install restart setup start test tidy clean install-deps lint

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

# First-time setup: builds, installs, and starts trd in a tmux session.
# Usage: make setup TELEGRAM_BOT_TOKEN=123456:ABCDEF...
setup: install
	@if [ -z "$(TELEGRAM_BOT_TOKEN)" ]; then \
		echo "Usage: make setup TELEGRAM_BOT_TOKEN=<your-token>"; \
		echo "Get a token from @BotFather on Telegram."; \
		exit 1; \
	fi
	cd channel && bun install
	@echo "Creating tmux session 'trd'..."
	tmux new-session -d -s trd 2>/dev/null || true
	tmux send-keys -t trd "export TELEGRAM_BOT_TOKEN=$(TELEGRAM_BOT_TOKEN)" Enter
	tmux send-keys -t trd "export TRD_CHANNEL_ENTRY=$(CURDIR)/channel/index.ts" Enter
	tmux send-keys -t trd 'trd start' Enter
	@echo ""
	@echo "TRD is running in tmux session 'trd'."
	@echo "  tmux attach -t trd     # see logs"
	@echo "  make restart            # rebuild + restart after code changes"
	@echo ""
	@echo "Your token and channel path are saved in the database."
	@echo "Future restarts need no env vars — just: make start"

# Start trd (reads saved config from database — no env vars needed after setup).
start: install
	tmux new-session -d -s trd 2>/dev/null || true
	tmux send-keys -t trd 'trd start' Enter
	@echo "TRD started in tmux session 'trd'. Attach with: tmux attach -t trd"

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
