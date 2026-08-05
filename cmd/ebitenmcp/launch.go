package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode"
)

// A game that fails to start is the most common thing that goes wrong, and it
// used to be the least visible.
//
// The output went to this server's stderr, which for a stdio MCP server is a log
// file the agent cannot read, and nobody waited for the process, so a package
// that did not compile looked exactly like one that hung: ninety seconds of
// polling a dead port and then "the game started but never answered". The
// compiler had said "undefined: foo" a second in.
//
// So the output is kept as well as passed through, and the exit is watched.

// tailWriter keeps the last few lines of a stream while still passing it on.
//
// Passing it on matters: somebody watching the terminal was already seeing this,
// and taking that away to give it to an agent would be a poor trade.
type tailWriter struct {
	mu      sync.Mutex
	through io.Writer
	partial []byte
	lines   []string
	max     int
}

func newTail(max int, through io.Writer) *tailWriter {
	return &tailWriter{max: max, through: through}
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.through != nil {
		t.through.Write(p)
	}

	t.partial = append(t.partial, p...)
	for {
		i := bytes.IndexByte(t.partial, '\n')
		if i < 0 {
			break
		}
		t.add(string(bytes.TrimRight(t.partial[:i], "\r")))
		t.partial = t.partial[i+1:]
	}

	return len(p), nil
}

func (t *tailWriter) add(line string) {
	t.lines = append(t.lines, line)
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
}

// Tail returns the last n lines, including whatever has been written without a
// newline yet — a panic cut off mid-line is exactly the case worth having.
func (t *tailWriter) Tail(n int) []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	lines := t.lines
	if len(t.partial) > 0 {
		lines = append(append([]string{}, lines...), string(t.partial))
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	out := make([]string, len(lines))
	copy(out, lines)
	return out
}

// Text is Tail as one block, for putting in an error message.
func (t *tailWriter) Text(n int) string {
	lines := t.Tail(n)
	if len(lines) == 0 {
		return "(it printed nothing)"
	}
	return strings.Join(lines, "\n")
}

// splitCommand splits a configured command the way a shell would, as far as
// quoting goes.
//
// strings.Fields was here before, which turned --start "go run . -level 'big
// map'" into an argument list nobody wrote and said nothing about it. Splitting
// on spaces is right until the first path or level name with one in it, and then
// it is wrong in a way that looks like the game's fault.
func splitCommand(s string) ([]string, error) {
	var (
		args    []string
		current strings.Builder
		quote   rune
		quoted  bool
	)

	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)

		case r == '\'' || r == '"':
			quote, quoted = r, true

		case unicode.IsSpace(r):
			// quoted keeps an explicit empty argument, which is not the same
			// thing as no argument at all.
			if quoted || current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
				quoted = false
			}

		default:
			current.WriteRune(r)
		}
	}

	if quote != 0 {
		return nil, fmt.Errorf("unbalanced %c in the start command %q", quote, s)
	}
	if quoted || current.Len() > 0 {
		args = append(args, current.String())
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("the start command is empty")
	}

	return args, nil
}
