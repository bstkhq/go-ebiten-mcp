package ebitenmcp

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// How the frames are played back, in one place rather than spelled into an
// ffmpeg filter string and a gif delay that nobody would think to compare.
//
// rawFPS is what the frames are declared at going in: one Draw per frame, and
// the recorder does not know how long each took, so a nominal 60 is as true as
// anything available. gifFPS is what a gif comes out at, which is lower on
// purpose — a gif carries its own palette and no motion compression, so twice
// the frames is roughly twice the file. fallbackGIFDelay is the one the pure-Go
// encoder writes instead, in hundredths of a second.
const (
	rawFPS           = 60
	gifFPS           = 25
	fallbackGIFDelay = 2
)

// saveVideo writes the recording as a file a person can watch.
//
// The format is worth choosing rather than defaulting. H.264 keeps the game's
// colours and costs almost nothing in size, which is what you want to actually
// watch something. A GIF is worse at both and plays inline in a README, an
// issue and most chat windows, which is what you want to show something.
//
// Both go through ffmpeg when it is there, because its generated palette makes
// a GIF that looks like the game rather than like 1998. Without ffmpeg only the
// standard library's fixed palette is left; it is noticeably worse, and a
// mediocre recording still beats a missing one.
//
// Every frame must be the size of the first. ffmpeg is fed a raw stream with the
// dimensions declared once up front, so a frame of another size does not produce
// a stretched picture — it shifts everything after it and the rest of the video
// is diagonal garbage. A window resized mid-recording is the way that happens.
func (s *Server) saveVideo(ctx context.Context, frames []*Frame, format string) (*Artifact, error) {
	if err := sameSize(frames); err != nil {
		return nil, err
	}

	if len(frames) == 0 {
		return nil, fmt.Errorf("nothing to encode")
	}

	_, err := exec.LookPath("ffmpeg")
	haveFFmpeg := err == nil

	switch format {
	case "gif":
		if haveFFmpeg {
			return s.encodeWithFFmpeg(ctx, frames, "gif")
		}
		return s.saveGIF(frames)

	case "", "mp4":
		if haveFFmpeg {
			return s.encodeWithFFmpeg(ctx, frames, "mp4")
		}
		// Asked for mp4 and cannot make one; a gif is closer to the intent
		// than an error.
		return s.saveGIF(frames)

	default:
		return nil, fmt.Errorf("unknown video format %q: mp4 or gif", format)
	}
}

// sameSize rejects a recording that changed shape part way through.
func sameSize(frames []*Frame) error {
	if len(frames) == 0 {
		return nil
	}

	want := frames[0].Image.Bounds()
	for i, f := range frames {
		if got := f.Image.Bounds(); got != want {
			return fmt.Errorf("frame %d is %v and the first is %v; the video is a raw stream "+
				"with one size declared up front, so a recording across a resize cannot be "+
				"encoded. Record again without resizing the window", i, got, want)
		}
	}
	return nil
}

func (s *Server) encodeWithFFmpeg(ctx context.Context, frames []*Frame, format string) (*Artifact, error) {
	b := frames[0].Image.Bounds()
	name := fmt.Sprintf("rec-%04d.%s", artifactSeq.Add(1), format)
	path := filepath.Join(s.media.dir, name)

	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", strconv.Itoa(b.Dx()) + "x" + strconv.Itoa(b.Dy()),
		"-r", strconv.Itoa(rawFPS),
		"-i", "-",
	}

	if format == "gif" {
		// One pass over the frames, generating a palette from them and applying
		// it. A GIF quantised against a palette built from the actual colours
		// looks like the game; the standard 216-colour web palette does not.
		args = append(args,
			"-filter_complex",
			fmt.Sprintf("[0:v] fps=%d,split [a][b];[a] palettegen=stats_mode=diff [p];"+
				"[b][p] paletteuse=dither=bayer:bayer_scale=3", gifFPS))
	} else {
		args = append(args,
			// yuv420p is what every player can decode, and it insists on even
			// dimensions, which a game's logical resolution does not promise.
			"-vf", "pad=ceil(iw/2)*2:ceil(ih/2)*2",
			"-pix_fmt", "yuv420p",
			"-c:v", "libx264",
			"-preset", "veryfast")
	}

	// With the context, so a stalled ffmpeg cannot hold the tool open for ever.
	// game_record already budgets for the frames it was asked for; before this
	// it computed that budget and then never passed it on.
	cmd := exec.CommandContext(ctx, "ffmpeg", append(args, path)...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	for _, f := range frames {
		if _, err := stdin.Write(f.Image.Pix); err != nil {
			break
		}
	}
	stdin.Close()

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v: %s", err, stderr.String())
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	// What came out, not what went in. A gif is resampled by the filter above,
	// so the count and the rate both change and the caller is entitled to know
	// by how much rather than assume it got what it asked for.
	kept, rate := len(frames), float64(rawFPS)
	if format == "gif" {
		rate = gifFPS
		kept = len(frames) * gifFPS / rawFPS
	}

	art := &Artifact{
		Path:   path,
		Kind:   format,
		Width:  b.Dx(),
		Height: b.Dy(),
		Bytes:  int(info.Size()),
		Frames: kept,
		FPS:    rate,
	}
	if s.media.baseURL != "" {
		art.URL = s.media.baseURL + "/media/" + name
	}
	return art, nil
}

func (s *Server) saveGIF(frames []*Frame) (*Artifact, error) {
	b := frames[0].Image.Bounds()

	out := &gif.GIF{}
	for _, f := range frames {
		p := image.NewPaletted(image.Rect(0, 0, b.Dx(), b.Dy()), palette.Plan9)
		draw.FloydSteinberg.Draw(p, p.Bounds(), f.Image, f.Image.Bounds().Min)

		out.Image = append(out.Image, p)
		out.Delay = append(out.Delay, fallbackGIFDelay)
	}

	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, out); err != nil {
		return nil, fmt.Errorf("encoding gif: %w", err)
	}

	art, err := s.media.save("rec", "gif", buf.Bytes(), b.Dx(), b.Dy())
	if err != nil {
		return nil, err
	}

	// Every frame, and faster than the ffmpeg path plays them. Nothing here
	// resamples, so this is the count that went in.
	art.Frames = len(frames)
	art.FPS = 100 / fallbackGIFDelay

	return art, nil
}
