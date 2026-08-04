package upstream

import (
	"crypto/sha256"
	"encoding/hex"
	"go/build"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This is the first line of defence for internal/hook's mirrored struct, and
// the only one that fires before anything has been written through it. The
// self-check in hook.Verify runs too late to prevent a bad write; a failure
// here is a compile-time-ish signal that the mirror needs updating.

var inputStateDecl = regexp.MustCompile(`(?s)type InputState struct \{.*?\n\}\n`)

func TestGoModPinsSupportedVersion(t *testing.T) {
	root := moduleRoot(t)

	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}

	want := Module + " " + Version
	if !strings.Contains(string(gomod), want) {
		t.Fatalf("go.mod does not require %q; internal/hook only mirrors that version", want)
	}
}

func TestInputStateLayout(t *testing.T) {
	path := filepath.Join(
		build.Default.GOPATH, "pkg", "mod",
		filepath.FromSlash(Module)+"@"+Version,
		"internal", "ui", "input.go",
	)

	src, err := os.ReadFile(path)
	if err != nil {
		// The harness container runs binaries without the module cache mounted.
		// Skipping there is fine: this check is meant for the host and for CI,
		// where the source is always at hand.
		t.Skipf("Ebitengine source not available at %s: %v", path, err)
	}

	decl := inputStateDecl.Find(src)
	if decl == nil {
		t.Fatalf("no InputState declaration found in %s", path)
	}

	sum := sha256.Sum256(decl)
	if got := hex.EncodeToString(sum[:]); got != InputStateSHA256 {
		t.Fatalf("ui.InputState changed in %s@%s\n"+
			"  got  %s\n  want %s\n\n"+
			"internal/hook mirrors this struct field by field. Compare the two,\n"+
			"update the mirror, then update InputStateSHA256.\n\n%s",
			Module, Version, got, InputStateSHA256, decl)
	}
}

// moduleRoot walks up from the test's directory to the directory holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
