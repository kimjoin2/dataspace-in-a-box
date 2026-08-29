GO ?= go

.PHONY: build test tck demo quickstart

build:
	$(GO) build -o dsbox ./cmd/dsbox

test:
	$(GO) test ./...

tck:
	./test/tck/run.sh
	$(GO) run ./cmd/tckgate tck-output.txt

demo:
	./demo/run.sh

# The quickstart document is the script: cmd/mdscript assembles one from the
# blocks in docs/quickstart.md, so the commands a reader follows and the
# commands CI runs are the same text. The generated script is kept rather
# than piped, because it is what a failure has to be read against.
quickstart:
	$(GO) run ./cmd/mdscript docs/quickstart.md quickstart.sh
	sh -e quickstart.sh
