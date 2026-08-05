# CLAUDE.md

Guidance for Claude Code working in this repository.

## What it is

A library that turns a running Ebitengine game into something an agent can see
and drive over MCP, plus `cmd/ebitenmcp`, which is everything around the game
the game cannot do for itself: give it a display, start it, and talk to it from
a shell.

The public surface is described in `README.md`; the mechanism, in
[docs/guide.md](docs/guide.md). Read the guide before changing capture, input
injection or the containerised display — all three have reasons that are not
visible from the code alone.

## Running the tests

**`make test`, not `go test`.** Ebitengine has no headless backend: it opens a
GLFW window while `internal/ui` initialises and panics without a `DISPLAY`,
before `main` runs. `make test` puts `ebitenmcp run` in front to provide one.

```sh
make test                              # the suite
make test TESTFLAGS='-count=1 -race'   # before committing
make test-gpu                          # the same on a real GPU
make test-nohook                       # without input injection; must stay green
make golden                            # re-record the example's golden images
```

`make test-nohook` builds with `-tags ebitenmcp_nohook`, which drops the
injection entirely. Everything that is not made of input has to keep passing:
a game that had to drop injection to build against a new Ebitengine still gets
its tests.

The gamepad tests need write access to `/dev/uinput`. Without it they skip and
say what to do about it.

## Two constraints that shape everything

**One game loop per process.** `ebiten.RunGame` cannot be called twice, so the
package has a single `TestMain` that owns the loop, and test isolation comes
from replacing the game with `SetGame` rather than restarting anything. Nothing
runs in parallel.

**The input injection mirrors an Ebitengine internal.** `internal/hook` writes
through a struct that must match `ui.InputState` field for field. Raising the
Ebitengine version in `go.mod` means re-reading `internal/ui/input.go` and
updating both the mirror and its checksum in `internal/upstream`, or the
self-check disables injection at startup.

## Where the guidance lives

Three places, and they do not overlap:

- **The tool descriptions** say when to reach for each tool and which to prefer.
  Anything specific to one tool belongs there.
- **`instructions` in `server.go`** reaches every client in the initialize
  response, so it is in context for a whole session whether they wanted it or
  not. Four things, and keep it that way.
- **`SKILL.md`** is the long form: how a whole session goes. Nothing installs
  it anywhere — the server sends the short version — so it is a document, and
  it is at the root to be read.

The mechanism behind any of it goes in `docs/guide.md`, not in these.

## Conventions

**Commits**: `scope: description in lower case`, no full stop. The scope is
where — a package, a file, a tool name, `*:` for the whole repo. Several scopes
go comma-separated. The body carries the why.

**Tests**: the name is a sentence asserting the claim —
`TestPauseHoldsTheGame`, not `TestPause`. The comment above says why the test
exists, not what the code does. Standard library only.

**Anything non-trivial gets checked by breaking the code and watching the test
fail.** Several tests in this repo passed against a bug until that was done.

**Comments explain why.** What the code does is in the code.
