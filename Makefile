GO ?= go

.PHONY: build test check quest demo run install package

build:
	$(GO) build -trimpath -o bin/questlock ./cmd/questlock

test:
	$(GO) test -race ./...

check:
	python3 -m unittest discover -s scripts -p 'test_*.py'
	python3 scripts/check-public-source.py
	$(GO) vet ./...
	$(GO) test -race ./...

quest: build
	./bin/questlock quest

demo: quest

run: build
	./bin/questlock serve

install:
	./scripts/install.sh

package:
	python3 scripts/package.py
