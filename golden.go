package ebitenmcp

import (
	"flag"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// RendererEnv carries which rasteriser a run is drawing with. `ebitenmcp run`
// sets it after asking the display; nothing sets it when a game is started by
// hand, and then the checks below simply have nothing to say.
const RendererEnv = "EBITENMCP_RENDERER"

// Golden image comparison, with three deliberate choices.
//
// First, the comparison has a tolerance. Ebitengine renders through whatever
// GPU or software rasteriser is present, and demanding identical bytes turns a
// regression suite into a machine-compatibility suite. Antialiased edges differ
// by a value or two between drivers and nobody cares.
//
// Second, a failure writes files. "images differ" is not a test failure anyone
// can act on; the expected image, what was actually drawn, and where they
// differ, all on disk, is.
//
// Third, a golden remembers which renderer drew it. The tolerance absorbs a
// plain scene across two rasterisers, and will not absorb antialiased text. When
// a comparison fails across renderers, that is very likely the whole story, and
// a failure that does not say so sends somebody looking through their own
// changes for a bug that is not there.

var (
	updateGoldenFlag *bool
	updateOnce       sync.Once
)

// registerGoldenFlag defines -update.
//
// It has to happen inside RunTests rather than in an init: testing parses flags
// at the top of m.Run, and a flag defined after that is simply not there. Doing
// it here also means every package init has already run, so a project that
// defines its own -update is detected rather than collided with.
func registerGoldenFlag() {
	updateOnce.Do(func() {
		if existing := flag.Lookup("update"); existing != nil {
			return
		}
		updateGoldenFlag = flag.Bool("update", false,
			"rewrite golden images instead of comparing against them")
	})
}

// UpdateGolden reports whether -update was given, in which case golden files
// are rewritten instead of compared.
func UpdateGolden() bool {
	if updateGoldenFlag != nil {
		return *updateGoldenFlag
	}

	// The project defined its own -update, so read theirs.
	if existing := flag.Lookup("update"); existing != nil {
		return existing.Value.String() == "true"
	}
	return false
}

// Golden compares the next frame against testdata/<name>.
//
// With -update it writes the file instead, which is how a golden is created and
// how an intended change is accepted.
func (d *Driver) Golden(name string) {
	d.t.Helper()
	d.GoldenImage(name, d.Screenshot())
}

// GoldenImage compares an image the caller already has.
func (d *Driver) GoldenImage(name string, got *image.RGBA) {
	d.t.Helper()

	path := filepath.Join("testdata", name)

	if UpdateGolden() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			d.t.Fatalf("creating %s: %v", filepath.Dir(path), err)
		}
		data, err := encodePNG(got)
		if err != nil {
			d.t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			d.t.Fatalf("writing %s: %v", path, err)
		}
		recordRenderer(path)

		d.t.Logf("golden updated: %s", path)
		return
	}

	wantFile, err := os.Open(path)
	if err != nil {
		d.writeArtifacts(path, got, nil)
		d.t.Fatalf("no golden at %s: run the test with -update to create it "+
			"(what was drawn is at %s)", path, path+".actual.png")
	}
	defer wantFile.Close()

	decoded, _, err := image.Decode(wantFile)
	if err != nil {
		d.t.Fatalf("reading %s: %v", path, err)
	}

	want := toRGBA(decoded)

	if want.Bounds() != got.Bounds() {
		d.writeArtifacts(path, got, want)
		d.t.Fatalf("%s: the game now draws %v, the golden is %v (see %s)",
			name, got.Bounds(), want.Bounds(), path+".actual.png")
	}

	differing := countDifferences(want, got, d.GoldenTolerance)
	total := got.Bounds().Dx() * got.Bounds().Dy()

	if fraction := float64(differing) / float64(total); fraction > d.GoldenMaxDiff {
		d.writeArtifacts(path, got, want)
		d.t.Errorf("%s: %d of %d pixels differ (%.3f%%, allowed %.3f%%)%s\n"+
			"  expected %s\n  actual   %s\n  diff     %s",
			name, differing, total, fraction*100, d.GoldenMaxDiff*100, rendererNote(path),
			path, path+".actual.png", path+".diff.png")
	}
}

// writeArtifacts drops the evidence next to the golden, so a failure can be
// looked at rather than reasoned about.
func (d *Driver) writeArtifacts(path string, got, want *image.RGBA) {
	d.t.Helper()

	if data, err := encodePNG(got); err == nil {
		os.WriteFile(path+".actual.png", data, 0o644)
	}
	if want == nil {
		return
	}

	comparison, _ := compareImages(want, got)
	if data, err := encodePNG(comparison); err == nil {
		os.WriteFile(path+".diff.png", data, 0o644)
	}
}

// recordRenderer notes what drew a golden, next to it, in a file a person can
// read and a diff can show.
func recordRenderer(path string) {
	current := os.Getenv(RendererEnv)
	if current == "" {
		return
	}
	os.WriteFile(path+".renderer", []byte(current+"\n"), 0o644)
}

// rendererNote explains a mismatch when there is one to explain, and says
// nothing at all when there is not.
func rendererNote(path string) string {
	current := os.Getenv(RendererEnv)

	recorded, err := os.ReadFile(path + ".renderer")
	if err != nil || current == "" {
		return ""
	}

	was := strings.TrimSpace(string(recorded))
	if was == current {
		return ""
	}

	return "\n  This golden was recorded with " + was + " and you are running with " +
		current + ", which is reason enough on its own. Re-record it here with -update, " +
		"or run where it was recorded."
}

func countDifferences(a, b *image.RGBA, tolerance int) int {
	differing := 0

	for i := 0; i < len(a.Pix) && i < len(b.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			if abs(int(a.Pix[i+c])-int(b.Pix[i+c])) > tolerance {
				differing++
				break
			}
		}
	}
	return differing
}

func toRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}

	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.Set(x, y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
