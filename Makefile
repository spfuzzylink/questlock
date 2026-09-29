GO ?= go

.PHONY: build test check demo run

build:
	$(GO) build -trimpath -o bin/agent-fence ./cmd/agent-fence

test:
	$(GO) test -race ./...

check:
	$(GO) vet ./...
	$(GO) test -race ./...

demo: build
	./bin/agent-fence demo

run: build
	./bin/agent-fence serve
