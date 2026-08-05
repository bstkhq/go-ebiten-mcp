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

A panic in the game is caught and recorded with its stack, its tick and the last
frame drawn. The game stops; the server keeps answering.

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

## Configuration

| | |
|---|---|
| `EBITEN_MCP_ADDR` | where to listen. Unset means do not serve |
| `EBITEN_MCP_CAPTURE` | `offscreen` to skip the screen-sized copy a final pass needs |
| `WithName` | what a client sees when several games are running |
| `WithFactory` | how to build a fresh game, for `game_reset` and the test driver |
| `WithCaptureStage` | the same as `EBITEN_MCP_CAPTURE`, in code |
| `WithState` | publish a named snapshot, reachable as `@name` |
| `WithAddr` | the address, ignoring the environment |

## Compatibility

Ebitengine **v2.9.9**, pinned. There are no tagged releases yet, so `@latest` is
the version to ask for.

| | Linux | macOS | Windows |
|---|---|---|---|
| see, drive, pause, inspect, profile | yes | yes | yes |
| `game_traces` | yes | yes | no |
| `game_gamepad` | yes | no | no |
| `ebitenmcp x` | yes | no | no |

Your MCP client needs tool support, and ideally image support — about half of
what comes back is a picture.

## More

- **[docs/guide.md](docs/guide.md)** — how the input injection works, running
  headless and in containers, golden images across renderers, gamepad
  identities, and what the capture stages cost.
- **`examples/playground`** — seven screens, each stressing one part of the
  system. `make run` starts it.

## Licence

MIT. Ebitengine is Apache-2.0 and stays that way: it is a dependency rather than
something this repository ships, so it carries its own terms wherever you get it
from.
