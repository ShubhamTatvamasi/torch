BINARY := bin/torch

.PHONY: build test fmt vet check install clean

build:
	go build -o $(BINARY) ./cmd/torch

test:
	go test ./...

fmt:
	gofmt -w .

vet:
	go vet ./...

check: fmt vet test

install:
	go install ./cmd/torch

clean:
	rm -rf bin
