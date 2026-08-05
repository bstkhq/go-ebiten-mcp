---
name: ebitenmcp
description: Look at and drive a running Ebitengine game through go-ebiten-mcp's tools — screenshots, synthetic input, game state, and the frames leading up to a crash. Use when the question is what the game actually does rather than what its source says, when a visual bug needs reproducing, or when the game has panicked, stalled or is dropping frames.
---

# Driving an Ebitengine game

The tools describe themselves; each says when to reach for it and which to
prefer. This is the part none of them can say on their own: how a session goes.

## Start by asking what state it is in

`game_state` first, always. It answers when the game itself has stopped, and it
distinguishes the three things that look identical from outside: running,
paused, and panicked. A tool that returns nothing because the loop is wedged is
not the same as a game with a bug, and guessing wrong costs a long detour.

If the game is not running at all, `game_start` runs it — bringing up a display
first on a machine that has none.

## Look before you reason

The source says what the game was meant to do. Reach for the tools when the
question is what it does:

- `game_screenshot` for the current frame.
- `game_record` for a grid of frames over time, which is how motion reads in a
  conversation. `frames` is how many it goes past, `every` is how many of those
  it keeps — twenty at one in five covers five times the ticks of four at one
  in one, for the same four pictures.
- `game_compare` when you have changed something and want the difference
  highlighted rather than described.

**Ask for `stage: "offscreen"` for anything you will compare between runs.** The
default is the final screen, which is what the player sees and is the size of
the window — so it changes with the monitor. The offscreen is the game's own
resolution.

## Drive it in one call, not three

Every input tool takes `then_wait_ticks` and `then_screenshot`. Pressing a key,
waiting for the game to react, and looking at the result is one call. Three
calls is three round trips and a race with the frame you wanted.

For anything longer than a single action, `game_script` runs a whole sequence
anchored to ticks and hands back a contact sheet of the moments you asked it to
capture. Ticks are relative to the start, so the same script works whenever it
runs — which is what makes it something you can attach to an issue.

## Wait for a condition, not for a number of ticks

`game_wait` blocks until a state path reaches a value or changes. That is what
makes a sequence deterministic. A string of `game_step` calls with guessed
counts is the same thing written worse, and it goes wrong the moment the machine
is busy.

It polls from outside the loop, so it samples roughly every other tick: use it
to wait for a state the game *enters and stays in*, not for a counter to pass
through one particular number.

## Switch the buffer on before you reproduce, not after

`game_frames` keeps a rolling buffer of what already happened, which is the only
way to see the frames *before* a crash or a glitch. It is off by default because
keeping frames costs the game time and memory.

The order is enable, reproduce, ask. Asking first gets you an empty buffer and
a second attempt.

## When it breaks

A panic in the game is caught. The game stops; the server keeps answering, and
the frame from the moment of the crash is kept.

- `game_state` has the crash: its value, its stack, the tick, and whether it was
  in update, draw or the final pass.
- `game_traces` has what the process printed, tagged with the tick it was
  written in, so the last lines before the panic are right there.
- `game_goroutines` is for the other failure: a game that has not panicked and
  is not advancing either.

None of those three touch the game loop, so they answer when everything else
would hang.

`game_reset` rebuilds the game from scratch without restarting the process,
which is how you carry on after a crash.

## Reading state the pixels do not show

`game_inspect` walks the game's own fields by path, unexported ones included,
because a Go game keeps almost everything unexported.

A path that does not exist answers with the fields that do — which is the fast
way to explore a structure you have not seen:

```
main.Game has no field "screen"; it has current, ticks, crt, crtShader, screens
```

A game can also publish computed snapshots, reachable as `@name`.

## Two things to keep in mind

**Showing a change is a screenshot, not a description of one.** If you have
made the game do something, return the picture.

**The port has no authentication.** Anyone who can reach it can read the game's
memory and type into it. It is for a machine you control, switched on to debug
and off afterwards.

---

## If you want this loaded as a skill

Nothing installs it for you, and that is deliberate: a copy in your repository
is right the day it is made and stale from the next release on, while the short
version comes from the game's own server every time a client connects, and is
always the version answering your calls.

This file is the long form, for reading. Copy it if you want it loaded:

```sh
mkdir -p .claude/skills/ebitenmcp
curl -fsSL -o .claude/skills/ebitenmcp/SKILL.md \
  https://raw.githubusercontent.com/bstkhq/go-ebiten-mcp/main/SKILL.md
```
