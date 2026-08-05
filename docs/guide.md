# go-ebiten-mcp: the guide

The parts that do not belong in a README: how the input injection reaches
Ebitengine, what capturing a frame costs, running without a display, and why a
golden image belongs to the renderer that drew it.

- [Capturing frames](#capturing-frames)
- [Publishing state a path cannot reach](#publishing-state-a-path-cannot-reach)
- [Tests](#tests)
- [Running without a display](#running-without-a-display)
- [Golden images belong to one renderer](#golden-images-belong-to-one-renderer)
- [Gamepads are real devices](#gamepads-are-real-devices)
- [How the input injection works](#how-the-input-injection-works)
- [Recording video](#recording-video)

## Capturing frames

Ebitengine draws twice. The game's `Draw` fills an offscreen at the logical
resolution; then, if the game implements `ebiten.FinalScreenDrawer`, its
`DrawFinalScreen` composites that onto the real screen. A CRT filter, scanlines,
a letterbox and a custom scaling filter all live in that second pass.

`game_screenshot` returns the final screen, which is what the player sees.
`stage: "offscreen"` asks for the one from before, which is how you tell a bug
in the game from a bug in the pass over it — and it is what tests should use,
since the final screen is the size of the window and a golden that changes with
the monitor is no golden at all.

### What it costs

Reading pixels back is a synchronisation point with the GPU, so it only happens
when somebody is waiting for a frame. Nothing at all is paid while nobody is
capturing, which is the line that matters for a game you have attached to and
are not looking at yet.

`game_frames` keeps the offscreen by default, where a screenshot keeps the final
screen. On a 480x320 game in a 960x640 window, on a Radeon RX 470:

| | mean draw time |
|---|---|
| not capturing | 279 µs |
| buffering the offscreen | 1.13 ms |
| buffering the final screen | 2.64 ms |
| switched off again | 283 µs |

The final screen is the size of the window rather than the logical resolution —
four times the pixels there, and the scale factor squared in general, so around
thirteen for a 480x320 game filling a 1080p screen. A screenshot pays it once
and it does not matter; a buffer running for ten minutes pays it thirty-six
thousand times.

`EBITEN_MCP_CAPTURE=offscreen` makes the offscreen the default everywhere, which
is worth setting when the final pass is expensive and you are not debugging it.

## Publishing state a path cannot reach

`game_inspect` walks the game's own fields by path. For the questions no path
answers, because the answer is computed rather than stored, a game can publish a
named snapshot:

```go
ebitenmcp.RunGame(game,
    ebitenmcp.WithState("summary", func(current ebiten.Game) any {
        g := current.(*Game)
        return map[string]any{"screen": g.screenName(), "score": g.score()}
    }),
)
```

Reachable as `@summary`. The provider is handed whatever game is running, not
the one that existed when it was written, so it keeps telling the truth after
`game_reset`.

## Tests

`RunTests` owns the process's single game loop and each test gets a freshly
built game. That shape is forced: `ebiten.RunGame` cannot be called twice in a
process, so isolation comes from replacing the game rather than restarting the
loop. If you have two tests calling `RunGame` in one package today, they are
sharing a binary and stepping on each other.

The driver reads state by path with `Inspect`, and with `WithGame` runs a
closure against the real type — inside the loop, which is the only place it is
safe to touch a running game. `ScreenshotStage` asks for a particular drawing
step; `Screenshot` gives the offscreen.

It holds the game still for a capture. Otherwise the frame caught is whichever
the loop drew next, and a tick counter or an animation on screen lands somewhere
different each run — so the test fails sometimes and passes others, which is
worse than no test at all.

Golden comparison has a tolerance, because Ebitengine renders through whatever
GPU or software rasteriser is present and demanding identical bytes turns a
regression suite into a machine-compatibility suite. A failure writes the
expected image, what was actually drawn, and a highlighted difference, next to
the golden.

## Running without a display

Ebitengine's `internal/ui` opens a GLFW window while it initialises and panics
without a `DISPLAY`, before `main` runs, so nothing inside the game can be early
enough to fix it. It has to be a process in front.

```sh
ebitenmcp run go test ./...
ebitenmcp run --gpu ./mygame
```

With `DISPLAY` already set it starts nothing — the desktop case, and the case of
a build container run with `--env=DISPLAY`. Without one it brings up a display
and says which renderer it ended up with, because a run that quietly fell back
to software still passes its tests while every timing it reports is a lie.

For a shell or a CI job `go install` is the right shape — you want the binary on
`$PATH`. For an MCP client it is not: use the `go run …@latest` form, which does
not depend on one. An MCP client started from a desktop launcher often does not
inherit the `$PATH` you see in a shell, which fails as "command not found" and
explains nothing.

### The display can live in a container, and the game does not

On a machine with no X and no way to install one, `ebitenmcp` runs the X server
in a container. The game does not go in there:

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
nothing you could do about it. This way the game is compiled and run wherever
you already do that, and the container has one job.

```sh
ebitenmcp x start [--gpu]    # prints the DISPLAY, and stays up between runs
ebitenmcp x status
ebitenmcp x stop
```

The image is built on demand from a Containerfile embedded in the binary, so
there is nothing to pull and nothing to publish. Weston covers both modes:
`--renderer=gl` draws on `/dev/dri/renderD128` with no privileges and no DRM
master, and `--renderer=pixman` needs no GPU at all. With `--gpu` the render
node is needed on *both* sides — the compositor uses it, and so does the game,
because with direct rendering it is the client that draws.

Two things worth knowing before they surprise you. The display has no access
control (`-ac`), because the client comes from outside the container and shares
no cookie; anyone on the machine can connect to it. And a container has its own
network namespace, so its abstract X socket cannot collide with the host's —
which is exactly why it must not be run with `--network host`.

For scale: a real 1080x1920 game runs at 3.8 ticks per second in software and
59.9 on a Radeon through the same path, drawing pixel-identical frames.

## Golden images belong to one renderer

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

Record goldens where they will be checked, and treat one as belonging to that
environment rather than to the repository at large.

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

The default profile is an Xbox 360 pad, whose id Ebitengine's controller
database has a complete mapping for, so the standard buttons work without
writing one. A known identity also takes its *name* from that database — you
cannot rename a controller the database recognises.

It is **Linux only**. A virtual input device is an operating system's own
business: Windows would need the ViGEmBus driver installed, and macOS a
DriverKit extension. Nothing else in this library is affected.

And a uinput device is **not a USB device**. It has no USB descriptors, so no
manufacturer or serial, and nothing enumerating USB will find it. That matters
only for a game that talks to its controller twice — once through Ebitengine and
once directly over USB HID. For one of those, pick a vendor the game does not
treat specially and everything input-related still works.

It needs write access to `/dev/uinput` and read access to `/dev/input/event*`,
which are usually root-only:

```sh
echo 'KERNEL=="uinput", GROUP="input", MODE="0660", OPTIONS+="static_node=uinput"' \
  | sudo tee /etc/udev/rules.d/99-uinput.rules
sudo gpasswd -a "$USER" input
```

Without them the gamepad tools say so and the tests skip; nothing else changes.

## How the input injection works

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
   Tests keep working too: a driver test that presses a key skips itself and
   names the tag, and one that only draws and reads still runs, so the golden
   images survive the escape hatch.

Raising the pinned Ebitengine version means re-reading `internal/ui/input.go`
and updating the mirror and its checksum in `internal/upstream`.

On legality: the `//go:linkname` restrictions added in Go 1.23 only cover
symbols defined in the standard library. Reaching into a third-party module is
still allowed, though [the proposal that introduced the
check](https://github.com/golang/go/issues/67401) says they would like to
require the handshake form everywhere eventually. The injection sits behind an
interface so that the day it closes, the implementation changes and the tools do
not.

### States and events

Ebitengine rebuilds the game-visible input state at the top of every tick from
what the window reported, and the injector writes over it once a tick. A key
held down or a cursor moved is a *state*: it is rewritten every tick until
something releases it. Typed runes and the wheel are *events*: they reach the
game for exactly one tick and are gone, like real ones.

That is why a touch is ended by sending the set that is still down, and all of
them by sending none — the injector stops writing and Ebitengine's own clearing
does the rest.

## Recording video

`game_record` returns a contact sheet — a grid of frames labelled with their
tick, which is how motion can be read in a conversation — and writes a video
next to it.

The two formats are not the same recording, and the answer says which you got:

| | frames | played at |
|---|---|---|
| `format: "mp4"` | every one recorded | 60 a second |
| `format: "gif"`, with ffmpeg | resampled | 25 a second |
| `format: "gif"`, without ffmpeg | every one recorded | 50 a second |

A gif carries its own palette and no motion compression, so twice the frames is
roughly twice the file; the resampling is what keeps one small enough to paste
into an issue. Without ffmpeg the fallback encoder uses the standard library's
fixed palette, which looks worse — and keeps every frame, so a recording made
where ffmpeg is missing is a different file from one made where it is not.

`inline: "gif"` returns the animation itself rather than the contact sheet.
Some clients animate it, some show a single frame, which is why it is asked for
rather than assumed.
