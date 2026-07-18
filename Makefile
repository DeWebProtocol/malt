.PHONY: all build build-verifier-wasm test vet clean

all: build

build:
	go build -buildvcs=false ./...

build-verifier-wasm:
	./scripts/build-verifier-wasm.sh dist/verifier

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf dist/
