package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSplitCommandKeepsQuotedArgumentsTogether(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{`go run ./cmd/mygame`, []string{"go", "run", "./cmd/mygame"}},
		{`go run . -level "big map"`, []string{"go", "run", ".", "-level", "big map"}},
		{`./game -name 'a b' -x 1`, []string{"./game", "-name", "a b", "-x", "1"}},
		{`  spaced   out  `, []string{"spaced", "out"}},
		{`./game -flag ""`, []string{"./game", "-flag", ""}},
		{`./game -path "/a b/c"`, []string{"./game", "-path", "/a b/c"}},
	} {
		got, err := splitCommand(tc.in)
		if err != nil {
			t.Errorf("splitting %q: %v", tc.in, err)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("splitting %q gave %q, want %q", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitting %q gave %q, want %q", tc.in, got, tc.want)
				break
			}
		}
	}
}

func TestSplitCommandRejectsWhatItCannotSplit(t *testing.T) {
	for _, in := range []string{`go run "unclosed`, ``, `   `} {
		if got, err := splitCommand(in); err == nil {
			t.Errorf("splitting %q gave %q, want an error saying why", in, got)
		}
	}
}

// TestTailKeepsTheEndAndPassesEverythingOn is the whole contract: the terminal
// still sees all of it, and what is kept is the end, because the end is where a
// process says why it is stopping.
func TestTailKeepsTheEndAndPassesEverythingOn(t *testing.T) {
	var through bytes.Buffer
	tail := newTail(3, &through)

	for _, line := range []string{"one", "two", "three", "four", "five"} {
		tail.Write([]byte(line + "\n"))
	}

	if got := strings.Join(tail.Tail(0), ","); got != "three,four,five" {
		t.Errorf("kept %q, want the last three", got)
	}
	if got := through.String(); got != "one\ntwo\nthree\nfour\nfive\n" {
		t.Errorf("passed on %q, want everything", got)
	}
}

// A panic arrives without a trailing newline often enough that losing the last
// line would lose the reason.
func TestTailIncludesAnUnfinishedLine(t *testing.T) {
	tail := newTail(10, nil)
	tail.Write([]byte("panic: boom\n"))
	tail.Write([]byte("goroutine 1 [running]:"))

	lines := tail.Tail(0)
	if len(lines) != 2 || lines[1] != "goroutine 1 [running]:" {
		t.Errorf("kept %q, want the unterminated line as well", lines)
	}
}

func TestTailSaysSoWhenThereIsNothing(t *testing.T) {
	if got := newTail(10, nil).Text(5); !strings.Contains(got, "nothing") {
		t.Errorf("an empty tail reads %q, which looks like output rather than the absence of it", got)
	}
}
