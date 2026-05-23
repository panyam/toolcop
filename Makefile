.PHONY: build test smoke fmt vet install clean

BINARY := toolcop
PREFIX ?= $(HOME)/.local

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o bin/$(BINARY) ./cmd/toolcop

test:
	go test ./...

smoke: build
	./tests/smoke.sh

fmt:
	go fmt ./...

vet:
	go vet ./...

install: build
	mkdir -p $(PREFIX)/bin
	install -m 0755 bin/$(BINARY) $(PREFIX)/bin/$(BINARY)

clean:
	rm -rf bin/
