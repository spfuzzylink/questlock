GO ?= go

.PHONY: build test check quest demo run

build:
	$(GO) build -trimpath -o bin/questlock ./cmd/questlock

test:
	$(GO) test -race ./...

check:
	python3 scripts/check-public-source.py
	$(GO) vet ./...
	$(GO) test -race ./...

quest: build
	./bin/questlock quest

demo: quest

run: build
	./bin/questlock serve
