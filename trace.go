package ebitenmcp

import (
	"bufio"
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

	restore []func()
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

	// Go's own os.Stdout still holds descriptor 1, which now points at the
	// pipe, so Go writes are captured along with everything else.
	go t.pump(r, passthrough, name, tick)

	t.mu.Lock()
	t.restore = append(t.restore, func() { dupTo(original, fd) })
	t.mu.Unlock()
}

func (t *traceRing) pump(r io.Reader, passthrough io.Writer, name string, tick func() int64) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		// The terminal gets it untouched, escapes and all. Only what is kept for
		// an agent is stripped, because there the colour is not colour: it is
		// half a dozen unreadable bytes per line, in a context window somebody
		// is paying for.
		if passthrough != nil {
			io.WriteString(passthrough, line+"\n")
		}
		t.add(name, stripANSI(line), tick())
	}
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
