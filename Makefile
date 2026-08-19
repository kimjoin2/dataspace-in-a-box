GO ?= go

.PHONY: build test tck demo

build:
	$(GO) build -o dsbox ./cmd/dsbox

test:
	$(GO) test ./...

tck:
	./test/tck/run.sh
	$(GO) run ./cmd/tckgate tck-output.txt

demo:
	./demo/run.sh
