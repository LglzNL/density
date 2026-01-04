BINARY=bin/densityctl

.PHONY: build test clean

build:
	mkdir -p bin
	go build -o $(BINARY) ./cmd/densityctl

test:
	go test ./...

clean:
	rm -rf bin results
