.DEFAULT_GOAL := help
SHELL := /bin/bash

BIN      := khata
LEDGER   ?= /tmp/khata-demo.jsonl
AAR      := android/app/libs/khata.aar
GOMOBILE := $(shell go env GOPATH)/bin/gomobile

.PHONY: help
help: ## show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

# --- Go ---------------------------------------------------------------------

.PHONY: build
build: ## build the CLI
	go build -o $(BIN) ./cmd/khata

.PHONY: test
test: ## run the Go test suite
	go test ./...

.PHONY: vet
vet: ## vet and format-check the Go tree
	go vet ./...
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi

.PHONY: bench
bench: ## run the parser and rule-engine benchmarks
	go test -run '^$$' -bench . -benchmem ./core/...

# --- Python toolchain -------------------------------------------------------

.PHONY: pytest
pytest: ## run the toolchain test suite
	cd toolchain && python3 -m pytest -q

.PHONY: eval
eval: build ## measure the parser against the labelled corpus
	cd toolchain && python3 -m khata_tools.cli eval --binary ../$(BIN) --min-accuracy 0.95

.PHONY: report
report: ## build a monthly report from the demo ledger
	cd toolchain && python3 -m khata_tools.cli report $(LEDGER)

# --- Android ----------------------------------------------------------------

.PHONY: aar
aar: ## build the engine AAR for Android (needs the NDK and gomobile)
	@command -v $(GOMOBILE) >/dev/null || { \
		echo "gomobile not found. Install it with:"; \
		echo "  go install golang.org/x/mobile/cmd/gomobile@latest && gomobile init"; exit 1; }
	mkdir -p $(dir $(AAR))
	$(GOMOBILE) bind -target=android -androidapi 26 \
		-javapkg=dev.khata.engine \
		-o $(AAR) \
		./core/mobile

.PHONY: apk
apk: aar ## build a debug APK
	@# The wrapper jar is a binary and is not committed; generate it on first use.
	@if [ ! -x android/gradlew ]; then \
		command -v gradle >/dev/null || { \
			echo "install Gradle 8.x, or run: cd android && gradle wrapper"; exit 1; }; \
		cd android && gradle wrapper --gradle-version 8.11.1; \
	fi
	cd android && ./gradlew --no-daemon assembleDebug

# --- demo -------------------------------------------------------------------

.PHONY: demo
demo: build ## import the sample corpus and print a month
	@rm -f $(LEDGER)
	./$(BIN) -ledger $(LEDGER) import testdata/sample_messages.txt
	@echo
	./$(BIN) -ledger $(LEDGER) budget groceries 4000 >/dev/null
	./$(BIN) -ledger $(LEDGER) budget food_delivery 1500 >/dev/null
	./$(BIN) -ledger $(LEDGER) budget transport 2000 >/dev/null
	./$(BIN) -ledger $(LEDGER) month 2025-08

.PHONY: check
check: vet test pytest eval ## everything CI runs, minus the Android build

.PHONY: clean
clean: ## remove build artifacts
	rm -f $(BIN) $(AAR)
	rm -rf toolchain/.pytest_cache toolchain/**/__pycache__
