VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test e2e lint

build:
	go build -ldflags '$(LDFLAGS)' -o bin/gaffer ./cmd/gaffer

install:
	go install -ldflags '$(LDFLAGS)' ./cmd/gaffer

test: lint
	go test ./...

e2e:
	./scripts/e2e.sh

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...
