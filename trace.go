package ebitenmcp

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// TraceLine is one line the process wrote.
type TraceLine struct {
	Tick   int64     `json:"tick"`
	Time   time.Time `json:"time"`
	Stream string    `json:"stream"`
	Text   string    `json:"text"`
}

const traceHistory = 2000

// traceRing keeps the last few thousand lines the process produced, tagged with
// the tick they were written in.
//
// The lines are captured at the file descriptor rather than through a logging
// adapter, which is what makes this work on a game that was never instrumented:
// fmt.Println, a library's logger, GLFW's warnings from C, and the runtime's
// own panic output all go through descriptors 1 and 2 and all end up here. An
// slog or logrus adapter only ever sees what the project remembered to route
// through it.
type traceRing struct {
	mu    sync.Mutex
	lines [traceHistory]TraceLine
	n     int

	taps []*fdTap
}

// fdTap is one redirected descriptor and everything needed to put it back.
//
// Putting it back is not optional. While the tap is in place, everything written
// to the descriptor goes through a pipe and a goroutine before reaching the
// terminal, and a process that calls os.Exit does not wait for that goroutine —
// so the tail of its output is simply lost. For a test binary that is the worst
// possible thing to lose, because the tail is the part that says which test
// failed and why.
type fdTap struct {
	fd       int
	original int
	read     *os.File
	write    *os.File
	restored *os.File // the original descriptor, wrapped so it can be closed portably
	drained  chan struct{}
}

func newTraceRing() *traceRing {
	return &traceRing{}
}

func (t *traceRing) add(stream, text string, tick int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.lines[t.n%traceHistory] = TraceLine{
		Tick:   tick,
		Time:   time.Now(),
		Stream: stream,
		Text:   text,
	}
	t.n++
}

// Lines returns the captured lines, oldest first.
func (t *traceRing) Lines() []TraceLine {
	t.mu.Lock()
	defer t.mu.Unlock()

	n := min(t.n, traceHistory)

	out := make([]TraceLine, 0, n)
	for i := t.n - n; i < t.n; i++ {
		out = append(out, t.lines[i%traceHistory])
	}
	return out
}

// capture redirects the process's stdout and stderr through this ring, still
// forwarding everything to wherever it was going before.
//
// Tee-ing rather than swallowing matters: a developer watching the terminal
// must keep seeing their own output, or the tool has quietly broken the thing
// it was meant to help with.
func (t *traceRing) capture(tick func() int64) {
	t.tap(1, "stdout", tick)
	t.tap(2, "stderr", tick)
}

func (t *traceRing) tap(fd int, name string, tick func() int64) {
	original, err := dupFD(fd)
	if err != nil {
		return
	}

	r, w, err := os.Pipe()
	if err != nil {
		return
	}

	if err := dupTo(int(w.Fd()), fd); err != nil {
		r.Close()
		w.Close()
		return
	}

	passthrough := os.NewFile(uintptr(original), "original-"+name)

	tap := &fdTap{fd: fd, original: original, read: r, write: w,
		restored: passthrough, drained: make(chan struct{})}

	// Go's own os.Stdout still holds descriptor 1, which now points at the
	// pipe, so Go writes are captured along with everything else.
	go func() {
		defer close(tap.drained)
		t.pump(r, passthrough, name, tick)
	}()

	t.mu.Lock()
	t.taps = append(t.taps, tap)
	t.mu.Unlock()
}

// stop puts the descriptors back and waits for everything already written to
// reach the terminal.
//
// The order is the whole point. Restoring the descriptor first means anything
// written from here on goes straight out; closing the pipe's write end then
// gives the pump its EOF, which is the only way it can ever return, since while
// the tap is up the write end *is* descriptor 1. Waiting for it is what stops a
// process that exits immediately afterwards from losing its last lines.
func (t *traceRing) stop() {
	t.mu.Lock()
	taps := t.taps
	t.taps = nil
	t.mu.Unlock()

	for _, tap := range taps {
		dupTo(tap.original, tap.fd)
		tap.write.Close()

		select {
		case <-tap.drained:
		case <-time.After(time.Second):
			// Something is still holding the write end open. Waiting forever
			// here would hang the shutdown of a process that is only trying to
			// tidy up, which is a worse outcome than a truncated last line.
		}

		tap.read.Close()
		tap.restored.Close()
	}
}

// maxKeptLine is how much of one line is worth remembering. The line itself is
// passed on whole; only the copy kept for an agent is cut.
const maxKeptLine = 8 << 10

// pump forwards everything and keeps what it can.
//
// It reads rather than scans, and the difference is the whole function.
// bufio.Scanner gives up on a line longer than its buffer and returns false, and
// this goroutine is the only thing draining a pipe that *is* descriptors 1 and
// 2 — so once it stopped, the pipe filled, and the next Print blocked forever.
// Inside Update that is the game frozen, by a log line, with the loop apparently
// alive and nothing to say why.
//
// So nothing here may stop reading short of EOF, and nothing may be dropped on
// the way to the terminal: a developer watching it must see their own output
// whole. Only what is stored is bounded.
func (t *traceRing) pump(r io.Reader, passthrough io.Writer, name string, tick func() int64) {
	reader := bufio.NewReaderSize(r, 64*1024)

	for {
		line, err := reader.ReadString('\n')

		if len(line) > 0 {
			// The terminal gets it untouched, escapes and all. Only what is kept
			// for an agent is stripped, because there the colour is not colour:
			// it is half a dozen unreadable bytes per line, in a context window
			// somebody is paying for.
			if passthrough != nil {
				io.WriteString(passthrough, line)
			}
			t.add(name, keep(strings.TrimSuffix(line, "\n")), tick())
		}

		if err != nil {
			return
		}
	}
}

func keep(line string) string {
	line = stripANSI(line)
	if len(line) <= maxKeptLine {
		return line
	}
	return line[:maxKeptLine] + fmt.Sprintf(" …[%d more bytes]", len(line)-maxKeptLine)
}

// stripANSI removes terminal control sequences.
//
// A game whose logger checks isatty keeps seeing one — the tee hands it the real
// descriptor — so it goes on emitting colour, and every captured line arrives
// wrapped in escapes. Deliberately only the escapes: the sequences go and the
// text stays exactly as it was, tabs, spacing, line breaks and all. Flattening a
// stack trace to make it tidy would throw away the reason it was captured.
func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] != 0x1b {
			b.WriteRune(runes[i])
			continue
		}

		// CSI (ESC [) runs to a byte in @ to ~; OSC (ESC ]) to a BEL or ESC \;
		// anything else is a two-byte sequence.
		switch {
		case i+1 < len(runes) && runes[i+1] == '[':
			i += 2
			for i < len(runes) && (runes[i] < '@' || runes[i] > '~') {
				i++
			}
		case i+1 < len(runes) && runes[i+1] == ']':
			i += 2
			for i < len(runes) && runes[i] != 0x07 && runes[i] != 0x1b {
				i++
			}
			if i < len(runes) && runes[i] == 0x1b && i+1 < len(runes) && runes[i+1] == '\\' {
				i++
			}
		default:
			i++
		}
	}

	return b.String()
}

// filter narrows the captured lines the way a caller asked.
func filterTraces(lines []TraceLine, stream, contains string, sinceTick int64, limit int) []TraceLine {
	out := make([]TraceLine, 0, len(lines))

	for _, l := range lines {
		if stream != "" && l.Stream != stream {
			continue
		}
		if contains != "" && !strings.Contains(l.Text, contains) {
			continue
		}
		if l.Tick < sinceTick {
			continue
		}
		out = append(out, l)
	}

	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}
