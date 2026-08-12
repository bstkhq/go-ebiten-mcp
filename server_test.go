package ebitenmcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoopbackDecidesWhoCanReachIt.
//
// There is no authentication anywhere in this package, so the address is the
// entire access control and getting this predicate wrong means either a warning
// nobody needs or, much worse, silence about a game the whole network can drive.
func TestLoopbackDecidesWhoCanReachIt(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8384", true},
		{"127.0.0.53:8384", true},
		{"[::1]:8384", true},
		{"localhost:8384", true},

		{"0.0.0.0:8384", false},
		{"[::]:8384", false},
		{":8384", false},
		{"192.168.1.10:8384", false},
		{"kiosk.local:8384", false},

		// Not an address at all. Warning about it is the safe way to be wrong.
		{"nonsense", false},
	} {
		if got := loopback(tc.addr); got != tc.want {
			t.Errorf("loopback(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

// TestTheArtifactsGoWhereTheyWereToldTo.
//
// The default is relative to the working directory, and a game started from a
// shell always has a writable one. An Android app's is / and an iOS app's is
// its own read-only bundle, so on a phone the default cannot be created —
// newMedia fails, Serve returns the error, and Wrap prints "not serving" and
// runs the game with no server at all. Which is a debugging tool that silently
// is not there.
func TestTheArtifactsGoWhereTheyWereToldTo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "somewhere", "the", "platform", "gave", "us")

	s, err := newServer(testRT, &Options{Name: "probe", MediaDir: dir}, dir, "")
	if err != nil {
		t.Fatalf("building the server: %v", err)
	}

	// Created on the way, since a platform hands out a directory and not
	// necessarily its parents.
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("it did not create %s: %v", dir, err)
	}

	art, err := s.media.save("shot", "png", []byte("not really a png"), 1, 1)
	if err != nil {
		t.Fatalf("saving: %v", err)
	}
	if !strings.HasPrefix(art.Path, dir) {
		t.Errorf("it wrote to %s, which is not under %s", art.Path, dir)
	}
}

// TestServeFallsBackToTheDefaultDirectory keeps the option optional: every game
// started from a shell should carry on writing where it always has.
func TestServeFallsBackToTheDefaultDirectory(t *testing.T) {
	t.Chdir(t.TempDir())

	server, err := Serve(testRT, &Options{Name: "probe", Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("serving: %v", err)
	}
	t.Cleanup(func() { server.Close() })

	if _, err := os.Stat(MediaDir); err != nil {
		t.Errorf("nothing created %s: %v", MediaDir, err)
	}
}
