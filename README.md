# go-ebiten-mcp

See, drive and inspect a running [Ebitengine](https://ebitengine.org) game over
[MCP](https://modelcontextprotocol.io) — and use the same machinery to write
visual regression tests.

An agent can take a screenshot of your game, press a key, read a field the game
never exported, and tell you what the screen looked like a second before it
crashed. So can a test.

## Use it

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

With `EBITEN_MCP_ADDR` unset that *is* `ebiten.RunGame` — no port, no goroutine
— so the line can stay in a release build. Setting it is what starts the server:

```sh
EBITEN_MCP_ADDR=127.0.0.1:8384 ./mygame
```

```json
{ "mcpServers": { "game": { "type": "http", "url": "http://127.0.0.1:8384/mcp" } } }
```

That is the whole setup. `ebitenmcp find` lists the games running locally.

If your client can only launch a command and speak over a pipe, use the control
server, which starts the game and forwards its tools:

```json
{ "mcpServers": { "game-control": {
    "command": "go",
    "args": ["run", "github.com/bstkhq/go-ebiten-mcp/cmd/ebitenmcp@latest",
             "mcp", "--start", "go run ./cmd/mygame"] } } }
```

`ebitenmcp init` writes that file for you and leaves a note in `CLAUDE.md`.

### Do not expose this

**There is no authentication.** Anyone who can open the port can read the game's
memory — unexported fields included — watch its screen and type into it.

- Never bind a public address.
- Do not leave it enabled in production. Turn it on to debug, turn it off after.
- Reach a remote game by forwarding the port:
  `ssh -L 8384:127.0.0.1:8384 kiosk`

## The tools

| | |
|---|---|
| **Look** | `game_screenshot` `game_record` `game_compare` `game_frames` |
| **Drive** | `game_key` `game_type` `game_mouse` `game_touch` `game_gamepad` `game_script` |
| **Control** | `game_pause` `game_resume` `game_step` `game_wait` `game_set_tps` `game_reset` |
| **Ask** | `game_inspect` `game_state` `game_input_state` `game_traces` `game_frametimes` `game_goroutines` `game_profile` |

`game_inspect` reads unexported fields, because a Go game keeps almost
everything unexported. `game_frames` is a rolling buffer, so the frames *before*
a crash are still there when you think to ask. `game_wait` blocks until a state
path reaches a value, which is what makes a sequence deterministic. Every input
tool takes `then_wait_ticks` and `then_screenshot`, so press-wait-look is one
call.

Panic recovery is opt-in. Set `EBITEN_MCP_RECOVER_PANICS=1` or pass
`WithPanicRecovery(true)` to catch a panic in the game and record its stack,
tick and last frame drawn; the game stops and the server keeps answering.
Without either, panics propagate normally.
`EBITEN_MCP_RECOVER_PANICS` does not turn the server on; that still requires
`EBITEN_MCP_ADDR`.

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

`RunTests` owns the process's single game loop and gives each test a freshly
built game. Golden comparison has a tolerance and writes the expected image, the
actual one and their difference on failure.

## When there is no display

Ebitengine has no headless backend, so it takes a process in front:

```sh
go install github.com/bstkhq/go-ebiten-mcp/cmd/ebitenmcp@latest

ebitenmcp run go test ./...      # your ebiten tests, headless
ebitenmcp run ./mygame           # the game, with the server on
ebitenmcp run --gpu ./mygame     # rendering on the GPU
```

With `DISPLAY` already set it changes nothing. Without one it starts a display,
in a container if the machine has no X — see the [guide](docs/guide.md).

`--gpu` is checked, not assumed: it refuses to start the command when `glxinfo`
cannot identify the renderer or reports llvmpipe/another software rasteriser.
That keeps a green test run from being mistaken for a valid GPU benchmark.

On macOS and Windows there is nothing to arrange: a game opens its own window,
so `run` only switches the server on and starts it.

## Configuration

| | |
|---|---|
| `EBITEN_MCP_ADDR` | where to listen. Unset means do not serve |
| `EBITEN_MCP_CAPTURE` | `offscreen` to skip the screen-sized copy a final pass needs |
| `EBITEN_MCP_RECOVER_PANICS` | `1` to catch game panics and keep MCP answering; off by default |
| `WithName` | what a client sees when several games are running |
| `WithFactory` | how to build a fresh game, for `game_reset` and the test driver |
| `WithCaptureStage` | the same as `EBITEN_MCP_CAPTURE`, in code |
| `WithState` | publish a named snapshot, reachable as `@name` |
| `WithAddr` | the address, ignoring the environment |
| `WithPanicRecovery` | override `EBITEN_MCP_RECOVER_PANICS` in code |
| `WithMediaDir` | where screenshots and video are written; the default is relative to the working directory |

## Compatibility

Ebitengine **v2.9.9**, pinned.

| | Linux | macOS | Windows | Android / iOS |
|---|---|---|---|---|
| see, drive, pause, inspect, profile | yes | yes | yes | yes |
| `game_traces` | yes | yes | no | builds |
| `game_gamepad` | yes | no | no | no |
| `ebitenmcp x` | yes | no | no | n/a |

CI runs the whole suite on Linux; on macOS and Windows it builds and runs what
needs no display, and for Android and iOS it builds. A tool that cannot do its
job on a platform says so — `game_traces` names the platform, `game_gamepad`
names the device it needs — rather than answering as though there were nothing
to report.

On mobile the entry point is different, and so are two of the defaults:

```go
wrapped, _ := ebitenmcp.Wrap(&Game{},
    ebitenmcp.WithAddr("127.0.0.1:8384"),          // no environment on a phone
    ebitenmcp.WithMediaDir(filesDir+"/ebitenmcp"), // the working directory is not writable
)
mobile.SetGame(wrapped)
```

`filesDir` is whatever the platform hands the app — `Context.getFilesDir()` on
Android, the app's Documents on iOS. Android needs the `INTERNET` permission in
its manifest for the listener.

`GOOS=js` compiles, and cannot serve: WebAssembly has no listening sockets. With
no address configured that costs nothing, so the line is safe to leave in a
browser build.

Your MCP client needs tool support, and ideally image support — about half of
what comes back is a picture.

## More

- **[docs/guide.md](docs/guide.md)** — how the input injection works, running
  headless and in containers, golden images across renderers, gamepad
  identities, and what the capture stages cost.
- **[SKILL.md](SKILL.md)** — how a debugging session goes: where to start, what
  to switch on before reproducing a problem, and which tools still answer once
  the game has stopped. The server sends the short version of it to every client
  that connects.
- **`examples/playground`** — seven screens, each stressing one part of the
  system. `make run` starts it.

## Licence

[MIT](LICENSE).
