# go-ebiten-mcp

See, drive and inspect a running [Ebitengine](https://ebitengine.org) game over
[MCP](https://modelcontextprotocol.io) — and use the same machinery to write
visual regression tests.

One line:

```go
import ebitenmcp "github.com/bstkhq/go-ebiten-mcp"

func main() {
    ebiten.SetWindowSize(640, 480)

    if err := ebitenmcp.RunGame(&Game{}); err != nil {   // was: ebiten.RunGame
        log.Fatal(err)
    }
}
```

Nothing happens until you ask for it. With `EBITEN_MCP_ADDR` unset, `RunGame` is
`ebiten.RunGame` and no socket is opened and no goroutine is started, so the line
can stay in a release build — and can be switched on there when you need it.

## Attaching to a game that is already running

This is the normal case, and it needs no tooling at all. The game runs where it
runs, with its own screen and its own GPU; the only thing it needs is the address
in its environment:

```sh
EBITEN_MCP_ADDR=127.0.0.1:8384 ./mygame
```

```json
{ "mcpServers": { "game": { "type": "http", "url": "http://127.0.0.1:8384/mcp" } } }
```

That is the whole setup for debugging a kiosk, a dev build, or anything else that
is already up. For a machine across the network the game listens on `0.0.0.0` and
the URL points at it; loopback stays the default, which is the right thing for a
line left in a shipped build. `ebitenmcp find` lists what is running locally.

If your client cannot talk to an HTTP server — plenty only know how to launch a
command and speak over a pipe — use the control server instead. It needs no
install of its own, and it forwards the game's tools as well as starting it:

```json
{ "mcpServers": { "game-control": {
    "command": "go",
    "args": ["run", "github.com/bstkhq/go-ebiten-mcp/cmd/ebitenmcp@latest",
             "mcp", "--start", "go run ./cmd/mygame"] } } }
```

`ebitenmcp init` writes that file for you, guessing the start command from the
project, and leaves a note in `CLAUDE.md` so an agent knows the tools are there.

The `go run` form is deliberate: `go install` puts a binary somewhere that has to
be on `$PATH`, and an MCP client started from a desktop launcher often does not
inherit the `$PATH` you see in a shell — which fails as "command not found" and
explains nothing. Go fetches and caches the build once.

## What you get
**Look at it.** `game_screenshot` returns the frame inline exactly as the player
sees it, and writes a lossless PNG. That means *after* the game's own
`DrawFinalScreen` if it has one — a CRT filter, scanlines, a letterbox, a custom
scaling filter all live in that second pass, and a screenshot taken before it
would show an image nobody ever saw. `stage: "offscreen"` asks for the one from
before instead, which is how you tell a bug in the game from a bug in the pass
over it. `game_record` returns a *contact sheet* —
a grid of frames labelled with their tick, which is how motion can actually be
read in a conversation — and writes a video next to it: `format: "mp4"` for
something to watch, `format: "gif"` for something that plays inline in a README
or an issue. A client that animates gifs can have one back directly, with
`inline: "gif"`. `game_compare` puts a before, an after and their difference in one
image.

**Drive it.** `game_key`, `game_type`, `game_mouse`, `game_touch` and
`game_gamepad` synthesise input the game cannot tell from the real thing: held keys have durations, drags
follow a path across ticks, several touches exist at once, typed text arrives as
characters rather than key presses. Each takes `then_wait_ticks` and
`then_screenshot`, so press-wait-look is one call rather than three.

**Stop it.** `game_pause`, `game_step`, `game_set_tps` and `game_wait` — the last
of which can wait on a state path reaching a value, which is what makes a
sequence deterministic instead of a string of sleeps.

**Repeat it.** `game_script` runs a whole sequence anchored to ticks in one call,
and hands back a contact sheet of the moments you asked it to capture. Ticks are
relative to the start, so the same script works whenever it runs — which is what
makes it something you can paste into an issue.

**See what led up to it.** `game_record` captures what comes next, which only
helps with a bug you already know how to reproduce. `game_frames` keeps a rolling
buffer so the frames *before* a crash are still there when you think to ask. It
is off by default: enable it, let it run, then ask. It reports what it is using
and how many frames it had to drop, because a buffer quietly keeping half of what
it was asked to keep would be worse than one that says so.

It also keeps the *offscreen* by default, where a screenshot keeps the final
screen, and the reason is measured rather than assumed. On a 480x320 game in a
960x640 window, on a Radeon RX 470:

| | mean draw time |
|---|---|
| not capturing | 279 µs |
| buffering the offscreen | 1.13 ms |
| buffering the final screen | 2.64 ms |
| switched off again | 283 µs |

Reading pixels back is a synchronisation point with the GPU, and the final screen
is the size of the window rather than the logical resolution — four times the
pixels in that measurement, and the scale factor squared in general, so around
thirteen for a 480x320 game filling a 1080p screen. A screenshot pays it once and
it does not matter; a buffer running for ten minutes pays it thirty-six thousand
times. Nothing at all is paid while nobody is capturing, which is the line that
matters for a game in production with the address left set.

**Ask it things.** `game_inspect` walks the game's own state by path,
*including unexported fields*, because a Go game keeps almost everything
unexported and an inspector that respected visibility would show empty structs.
`game_state`, `game_traces` and `game_frametimes` cover the process: framerate,
memory, what it printed, and what each tick cost split into update and draw.
`game_profile` goes one further and says *where* the time or the memory went — it
returns the profile summarised as text, not just a pprof file somebody else would
have to open.

**And when it breaks.** A panic in the game is caught, recorded with its stack,
the tick it happened on and the lines it printed in the second before it — no
second call needed — and the frame from the moment of the crash is kept.
The game stops; the server keeps answering. `game_state`, `game_traces` and
`game_goroutines` never touch the game loop, so they still work when it is
deadlocked — and they say so, rather than hanging along with it.

## Tests

The same machinery without the protocol:

```go
func TestMain(m *testing.M) {
    ebitenmcp.RunTests(m, func() ebiten.Game { return NewGame() })
}

func TestMenuMovesOneRowPerPress(t *testing.T) {
    d := ebitenmcp.T(t)

    d.Tap(ebiten.KeyArrowDown)
    if got := d.Inspect("screens.menu.selected"); got != int64(1) {
        t.Fatalf("selection is %v, want 1", got)
    }

    d.Golden("menu_second_row.png")   // -update records it
}
```

`RunTests` owns the process's single game loop and each test gets a freshly
built game. That shape is forced: `ebiten.RunGame` cannot be called twice in a
process, so isolation comes from replacing the game rather than restarting the
loop. If you have two tests calling `RunGame` in one package today, they are
sharing a binary and stepping on each other; this fixes that as a side effect.

Golden comparison has a tolerance, because Ebitengine renders through whatever
GPU or software rasteriser is present and demanding identical bytes turns a
regression suite into a machine-compatibility suite. A failure writes the
expected image, what was actually drawn, and a highlighted difference, next to
the golden.

It also holds the game still for the capture. Otherwise the frame caught is
whichever the loop drew next, and a tick counter or an animation on screen lands
somewhere different each run — so the test fails sometimes and passes others,
which is worse than no test at all.

## When there is no screen: tests, CI, an agent

Ebitengine has no headless backend — `internal/ui` opens a GLFW window while it
initialises and panics without a `DISPLAY`, before `main` runs, so nothing inside
the game can be early enough to fix it. It has to be a process in front:

```sh
go install github.com/bstkhq/go-ebiten-mcp/cmd/ebitenmcp@latest

ebitenmcp run go test ./...      # your ebiten tests, headless
ebitenmcp run ./mygame           # the game, with the server on
ebitenmcp run --gpu ./mygame     # rendering on the GPU
ebitenmcp init                   # write .mcp.json and a note in CLAUDE.md
```

For a shell or a CI job `go install` is the right shape — you want the binary on
`$PATH`. For an MCP client it is not: use the `go run …@latest` form above, which
does not depend on one.

With `DISPLAY` already set it starts nothing and changes nothing — which is the
desktop case, and the case of a build container run with `--env=DISPLAY`. Without
one it brings up a display, and says which renderer it ended up with, because a
run that quietly fell back to software still passes its tests while every timing
it reports is a lie.

### The display can live in a container, and the game does not

On a machine with no X and no way to install one, `ebitenmcp` runs the X server in
a container. The game does not go in there:

```
   your environment                container
   ┌──────────────────┐          ┌──────────────────────┐
   │ the game         │          │ weston headless      │
   │ + its Mesa, its  │──socket──│ + Xwayland           │
   │   libX11, its GL │   unix   │                      │
   └──────────────────┘          └──────────────────────┘
            └──────── /tmp/.X11-unix ────────┘
```

That split is the point. A container that also built or ran the game would link
it against one set of libraries and run it against another, and there would be
nothing you could do about it. This way the game is compiled and run wherever you
already do that, and the container has one job.

```sh
ebitenmcp x start [--gpu]    # prints the DISPLAY, and stays up between runs
ebitenmcp x status
ebitenmcp x stop
```

The image is built on demand from a Containerfile embedded in the binary, so
there is nothing to pull and nothing to publish. Weston covers both modes:
`--renderer=gl` draws on `/dev/dri/renderD128` with no privileges and no DRM
master, and `--renderer=pixman` needs no GPU at all. With `--gpu` the render node
is needed on *both* sides — the compositor uses it, and so does the game, because
with direct rendering it is the client that draws.

Two things worth knowing before they surprise you. The display has no access
control (`-ac`), because the client comes from outside the container and shares no
cookie; anyone on the machine can connect to it. And a container has its own
network namespace, so its abstract X socket cannot collide with the host's — which
is exactly why it must not be run with `--network host`.

Measured on one machine, for scale: a real 1080x1920 game runs at 3.8 ticks per
second in software and 59.9 on a Radeon through the same path, drawing
pixel-identical frames.

### Golden images belong to one renderer

A golden recorded under llvmpipe and compared under radeonsi is comparing two
rasterisers as much as two versions of the game. The tolerance absorbs a plain
scene; antialiased text and gradients it will not.

So `d.Golden` records which renderer drew it, in a file next to the image, and
says so when a comparison fails across renderers:

```
menu_second_row.png: 14406 of 153600 pixels differ (9.379%, allowed 0.100%)
  This golden was recorded with AMD Radeon RX 470 Graphics (radeonsi) and you are
  running with llvmpipe, which is reason enough on its own. Re-record it here with
  -update, or run where it was recorded.
```

Record goldens where they will be checked, usually CI, and treat one as belonging
to that environment rather than to the repository at large.

## The example

`examples/playground` is the test bed: seven screens, each stressing one part of
the system, including a screen with a slider that sinks the framerate, a panic
button and a button that blocks `Update` for three seconds. Those last two are
not decoration — the behaviour that matters most is what happens when the game
misbehaves, and that cannot be tested on a game that never does.

```sh
make run                          # the playground, display and all
ebitenmcp tools
ebitenmcp shot /tmp/frame.png
ebitenmcp call game_key '{"keys":["arrowdown"],"then_screenshot":true}'
```

`cmd/ebitenmcp` is a small client for a shell or a CI job, for when speaking the
protocol is not worth it.

## Gamepads are real devices

`game_gamepad` does not fake a controller inside Ebitengine — it asks the kernel
for one, through `/dev/uinput`, and Ebitengine finds it the way it finds a
controller somebody plugged in. Everything downstream behaves accordingly: the
SDL id, the standard-layout mapping, `inpututil`'s edges.

The identity is yours to choose, and that is the point rather than a flourish.
Games routinely decide what a controller *is* from its vendor id, so one that
could only claim to be an Xbox pad would send those games down a different path
than their own hardware and prove nothing:

```json
{"connect": {"name": "Advanced Gamepad", "vendor": "0x2a", "product": "0x01"}}
{"buttons": {"a": true}, "axes": {"leftx": -1.0}, "dpad": "right"}
```

The default profile is an Xbox 360 pad, whose id Ebitengine's controller database
has a complete mapping for, so the standard buttons work without writing one.
Note that a known identity also takes its *name* from that database — you cannot
rename a controller the database recognises.

Two limits worth knowing before they surprise you.

It is **Linux only**. A virtual input device is an operating system's own
business: Windows would need the ViGEmBus driver installed, and macOS a DriverKit
extension. Nothing else in this library is affected — keyboard, mouse and touch
work wherever Ebitengine does.

And a uinput device is **not a USB device**. It has no USB descriptors, so no
manufacturer or serial, and nothing enumerating USB will find it. That matters
only for a game that talks to its controller twice — once through Ebitengine and
once directly over USB HID for whatever else the hardware does. For one of those,
pick a vendor the game does not treat specially and everything input-related
still works.

It needs write access to `/dev/uinput` and read access to `/dev/input/event*`,
which are usually root-only:

```sh
echo 'KERNEL=="uinput", GROUP="input", MODE="0660", OPTIONS+="static_node=uinput"' \
  | sudo tee /etc/udev/rules.d/99-uinput.rules
sudo gpasswd -a "$USER" input
```

Without them the gamepad tools say so and the tests skip; nothing else changes.

## How the input injection works, and what that costs

Ebitengine exposes no way to inject input. `ebiten.IsKeyPressed` and everything
in `inpututil` read from `internal/inputstate`, which only the window backend
writes to. `internal/hook` reaches in with `//go:linkname` and writes the same
state the backend does, from inside `Update`, so the game sees a real press in
the tick it expects one.

That means a struct in this module mirrors `ui.InputState` field by field, and a
field added upstream would silently move every offset after it. Three things
guard it, in the order they fire:

1. A test hashes the upstream declaration in the module cache. If Ebitengine
   changes the struct, the test fails — before anything writes through it.
2. Before the first tick, a self-check writes a pattern through the mirror and
   reads it back through Ebitengine's public API. If it does not match,
   injection is disabled with a reason and everything else keeps working.
3. `-tags ebitenmcp_nohook` drops the linkname and the mirror entirely. The game
   still builds and runs and loses only input injection, so an Ebitengine
   release this module has not been checked against can never block a build.

On legality: the `//go:linkname` restrictions added in Go 1.23 only cover symbols
defined in the standard library. Reaching into a third-party module is still
allowed, though [the proposal that introduced the
check](https://github.com/golang/go/issues/67401) says they would like to
require the handshake form everywhere eventually. The injection sits behind an
interface so that the day it closes, the implementation changes and the tools do
not.

## Compatibility

### Ebitengine

Pinned to **v2.9.9**. The range is narrow on purpose, for the reason above:
raising it means re-reading `internal/ui/input.go` and updating the mirror and
its checksum in `internal/upstream`. There are no tagged releases yet, so
`@latest` is the version to ask for.

### Platforms

Everything goes through Ebitengine except two things, and those two are the ones
that reach outside the process:

| | Linux | macOS | Windows |
|---|---|---|---|
| see, drive, pause, inspect, profile | yes | yes | yes |
| `game_traces` | yes | yes | **no** — it captures descriptors 1 and 2, which needs a unix-like platform |
| `game_gamepad` | yes | **no** | **no** — a virtual pad is a `/dev/uinput` device |
| `ebitenmcp x` | yes | **no** | **no** — a containerised X server |

Keyboard, mouse and touch are not in that list on purpose: they go through the
mirror rather than through the operating system, so they work wherever Go does.

### Your MCP client

Two things matter, and neither is about this project:

- **Tools.** A client with no tool support cannot call any of this. The
  [official client list](https://modelcontextprotocol.io/clients) has a column
  for it, and is worth checking there rather than in a copy here that would go
  stale.
- **Images.** About half of what comes back is a picture — screenshots, contact
  sheets, difference images, gifs. A client can support tools perfectly and still
  render only text, and then all of that arrives as a file path. Where a client
  does animate gifs, `game_record` with `inline: "gif"` is worth using; where one
  does not, the contact sheet says more, which is why it is the default.

Transport is not on that list any more: a client that only speaks stdio can use
`game-control`, which forwards the game's own tools over its own session.

## Licence

Apache-2.0, the same as Ebitengine.
