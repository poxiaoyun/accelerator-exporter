.PHONY: build test vet

build:
	mkdir -p bin
	go build -o bin/accelerator-exporter ./cmd/accelerator-exporter

test:
	go test ./...

vet:
	go vet ./...
