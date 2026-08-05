# Everything here is a plain Go command with `ebitenmcp run` in front of it, so
# that this repository is tested the way a project using it would be. No build
# image, no mounting, no copying binaries between environments: the tests
# compile and run here, and only the display comes from somewhere else.

SCREEN ?= 1280x720
ADDR   ?= 127.0.0.1:8384

# --addr "" leaves the MCP server switched off. Tests drive the game in-process
# through the driver; a server would only be a port for nobody.
RUN = go run ./cmd/ebitenmcp run --screen $(SCREEN) --addr ""

.PHONY: all
all: build test

.PHONY: build
build:
	go build ./...
	go vet ./...

# The escape hatch, which has to keep working and not merely keep compiling.
# Building it only ever proved it linked, and under it the suite was red: the
# driver refused to start a test at all when it could not inject, so a game that
# had dropped injection to build against a new Ebitengine also lost the tests
# that never touched a key. Everything not made of input has to pass here.
.PHONY: test-nohook
test-nohook:
	go build -tags ebitenmcp_nohook ./...
	$(RUN) -- go test -count=1 -tags ebitenmcp_nohook ./...

.PHONY: test
test:
	$(RUN) -- go test ./... $(TESTFLAGS)

# Re-records the golden images. It names a package rather than ./... because
# -update is registered by RunTests, so only a package that uses it knows the
# flag; the rest would refuse to start.
.PHONY: golden
golden:
	$(RUN) -- go test ./examples/playground -update

# The same suite on the GPU. Worth running before trusting any timing, and worth
# knowing that a golden recorded under one renderer may not match under another.
#
# -count=1 is not optional. Go's test cache tracks the environment variables a
# test reads, and DISPLAY is not one of them: ebiten picks it up through cgo, so
# a cached result from the software run would be served here and this target
# would quietly test nothing at all.
.PHONY: test-gpu
test-gpu:
	$(RUN) --gpu -- go test -count=1 ./... $(TESTFLAGS)

.PHONY: run
run:
	go run ./cmd/ebitenmcp run --screen $(SCREEN) --addr $(ADDR) -- go run ./examples/playground

.PHONY: run-gpu
run-gpu:
	go run ./cmd/ebitenmcp run --gpu --screen $(SCREEN) --addr $(ADDR) -- go run ./examples/playground

# Stops the containerised X server, if one was left behind.
.PHONY: x-stop
x-stop:
	go run ./cmd/ebitenmcp x stop

.PHONY: clean
clean:
	rm -rf .bin
