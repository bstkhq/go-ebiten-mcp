package ebitenmcp

import (
	"bytes"
	"image"
	"image/gif"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The encoder, which was the only production file in the package with nothing
// running through it at all.
//
// None of this needs a display, a game or a loop: saveVideo takes the frames
// already made, so they can be drawn here in a few lines. That it was untested
// anyway is the usual reason — it sits behind game_record, which is slow, and
// so nobody reached it.
//
// The media directory comes from newTestServer, which uses t.TempDir, so
// everything written here goes away with the test.

// frameSeq makes a recording of n frames, each a solid colour a step lighter
// than the last, so an encoder that dropped or reordered them shows it.
func frameSeq(t *testing.T, n, width, height int) []*Frame {
	t.Helper()

	frames := make([]*Frame, 0, n)
	for i := 0; i < n; i++ {
		img := image.NewRGBA(image.Rect(0, 0, width, height))
		shade := uint8(20 + i*20)

		for p := 0; p < len(img.Pix); p += 4 {
			img.Pix[p], img.Pix[p+1], img.Pix[p+2], img.Pix[p+3] = shade, 0x40, 0x80, 0xff
		}

		frames = append(frames, &Frame{
			Tick:  int64(i),
			Time:  time.Now(),
			Stage: StageOffscreen,
			Image: img,
		})
	}
	return frames
}

// TestSameSizeRejectsARecordingThatChangedShape is the check whose failure mode
// is worth stating: the frames go to ffmpeg as a raw stream with one size
// declared up front, so a frame of another size does not come out stretched —
// it shifts every byte after it and the rest of the video is diagonal garbage.
// A window resized part way through a recording is how that happens, and the
// error has to name the frame rather than produce the garbage.
func TestSameSizeRejectsARecordingThatChangedShape(t *testing.T) {
	frames := frameSeq(t, 3, 8, 8)
	frames[2].Image = image.NewRGBA(image.Rect(0, 0, 8, 9))

	err := sameSize(frames)
	if err == nil {
		t.Fatal("a recording that changed size encoded anyway")
	}
	if !strings.Contains(err.Error(), "frame 2") {
		t.Errorf("the error does not say which frame: %v", err)
	}

	if err := sameSize(frameSeq(t, 3, 8, 8)); err != nil {
		t.Errorf("a recording that did not change size was refused: %v", err)
	}

	// Nothing to disagree about, so nothing to refuse. saveVideo has its own
	// answer for an empty recording and it is not this one.
	if err := sameSize(nil); err != nil {
		t.Errorf("an empty recording was refused: %v", err)
	}
}

func TestSaveVideoWritesTheFormatItWasAskedFor(t *testing.T) {
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skipf("no ffmpeg here, so only the fallback path exists: %v", err)
	}

	for _, c := range []struct {
		format string
		want   string
	}{
		{"gif", "gif"},
		{"mp4", "mp4"},
		// Nothing said, and the tool's own default is what a caller who did not
		// choose should get.
		{"", "mp4"},
	} {
		art, err := s.saveVideo(ctx, frameSeq(t, 4, 16, 16), c.format)
		if err != nil {
			t.Fatalf("saving %q: %v", c.format, err)
		}
		if art.Kind != c.want {
			t.Errorf("%q produced a %s, want %s", c.format, art.Kind, c.want)
		}
		if art.Bytes == 0 {
			t.Errorf("%q produced an empty file", c.format)
		}
		if art.Width != 16 || art.Height != 16 {
			t.Errorf("%q reports %dx%d, want 16x16", c.format, art.Width, art.Height)
		}
		if _, err := os.Stat(art.Path); err != nil {
			t.Errorf("%q says it wrote %s and did not: %v", c.format, art.Path, err)
		}
	}
}

func TestSaveVideoRefusesAFormatItCannotMake(t *testing.T) {
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	_, err := s.saveVideo(ctx, frameSeq(t, 2, 8, 8), "webm")
	if err == nil {
		t.Fatal("it accepted a format it does not know")
	}
	// Naming the two that work is the difference between an error somebody can
	// act on and one they have to go and read the source for.
	if !strings.Contains(err.Error(), "mp4") || !strings.Contains(err.Error(), "gif") {
		t.Errorf("the error does not say what would work: %v", err)
	}

	if _, err := s.saveVideo(ctx, nil, "gif"); err == nil {
		t.Error("it encoded a recording with no frames in it")
	}
}

// TestSaveVideoFallsBackToAGifWithoutFFmpeg.
//
// mp4 needs ffmpeg and a gif does not, so a machine without it can still have
// something to look at. Returning an error instead would fail the whole tool
// over the one part of it that is optional — and the frames, which are the
// point, are already in hand by then.
func TestSaveVideoFallsBackToAGifWithoutFFmpeg(t *testing.T) {
	s := newTestServer(t)

	ctx, cancel := testContext(t)
	defer cancel()

	// LookPath is what saveVideo asks, so an empty PATH is a machine with no
	// ffmpeg for as long as this test runs.
	t.Setenv("PATH", "")

	art, err := s.saveVideo(ctx, frameSeq(t, 3, 8, 8), "mp4")
	if err != nil {
		t.Fatalf("asking for mp4 without ffmpeg: %v", err)
	}
	if art.Kind != "gif" {
		t.Errorf("it produced a %s; without ffmpeg a gif is the closest thing to the intent", art.Kind)
	}
}

// TestSaveGIFProducesAGifWithEveryFrameInIt covers the path taken when there is
// no ffmpeg: the standard library's palette rather than one generated from the
// game's own colours. It looks worse, and a mediocre recording still beats a
// missing one — but only if it is a gif somebody can actually open.
func TestSaveGIFProducesAGifWithEveryFrameInIt(t *testing.T) {
	s := newTestServer(t)

	art, err := s.saveGIF(frameSeq(t, 5, 12, 12))
	if err != nil {
		t.Fatalf("saving a gif: %v", err)
	}

	data, err := os.ReadFile(art.Path)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}

	decoded, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the gif it wrote does not decode: %v", err)
	}
	if len(decoded.Image) != 5 {
		t.Errorf("the gif holds %d frames, want 5", len(decoded.Image))
	}

	// Every frame needs a delay of its own; a gif whose delays do not match its
	// frames plays at whatever speed the viewer decides.
	if len(decoded.Delay) != len(decoded.Image) {
		t.Errorf("%d frames and %d delays", len(decoded.Image), len(decoded.Delay))
	}

	if got := decoded.Image[0].Bounds(); got.Dx() != 12 || got.Dy() != 12 {
		t.Errorf("the gif is %v, want 12x12", got)
	}
}
