package upstream

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
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
	path := filepath.Join(moduleDir(t), "internal", "ui", "input.go")

	src, err := os.ReadFile(path)
	if err != nil {
		// Deliberately not a skip.
		//
		// This is the only check that fires before anything is written through
		// the mirror, and a skip is indistinguishable from a pass in every
		// report anybody reads. The thing it guards against is writing at the
		// wrong offsets inside another package's struct, so being quietly absent
		// is the one behaviour it must not have. If the source is missing, say
		// so and fail.
		t.Fatalf("cannot read %s: %v\n\n"+
			"This check hashes Ebitengine's own declaration, so it needs the source. "+
			"Run `go mod download %s` and try again. Do not skip it: it is what stands "+
			"between internal/hook and writing through a stale struct.", path, err, Module)
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

// moduleDir asks the toolchain where the pinned Ebitengine actually is.
//
// Guessing at GOPATH/pkg/mod was near enough and not right: it ignores
// GOMODCACHE, a vendor directory and any replace directive, so on a machine set
// up in any of those ways the file would be missing and — before this stopped
// being a skip — the check would have quietly passed.
func moduleDir(t *testing.T) string {
	t.Helper()

	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", Module).Output()
	if err != nil {
		t.Fatalf("asking go where %s is: %v", Module, err)
	}

	dir := strings.TrimSpace(string(out))
	if dir == "" {
		t.Fatalf("go could not say where %s is; run `go mod download %s`", Module, Module)
	}
	return dir
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
