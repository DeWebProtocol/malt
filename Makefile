.PHONY: all build build-malt build-cas build-malt-eval test vet clean

all: build

build: build-malt build-cas build-malt-eval

build-malt:
	go build -buildvcs=false -o bin/malt ./cmd/malt

build-cas:
	go build -buildvcs=false -o bin/cas ./cmd/cas

build-malt-eval:
	go build -buildvcs=false -o bin/malt-eval ./cmd/eval/malt-eval

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf bin/
