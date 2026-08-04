package ebitenmcp

import (
	"bytes"
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
func (s *Server) saveVideo(frames []*Frame, format string) (*Artifact, error) {
	if len(frames) == 0 {
		return nil, fmt.Errorf("nothing to encode")
	}

	_, err := exec.LookPath("ffmpeg")
	haveFFmpeg := err == nil

	switch format {
	case "gif":
		if haveFFmpeg {
			return s.encodeWithFFmpeg(frames, "gif")
		}
		return s.saveGIF(frames)

	case "", "mp4":
		if haveFFmpeg {
			return s.encodeWithFFmpeg(frames, "mp4")
		}
		// Asked for mp4 and cannot make one; a gif is closer to the intent
		// than an error.
		return s.saveGIF(frames)

	default:
		return nil, fmt.Errorf("unknown video format %q: mp4 or gif", format)
	}
}

func (s *Server) encodeWithFFmpeg(frames []*Frame, format string) (*Artifact, error) {
	b := frames[0].Image.Bounds()
	name := fmt.Sprintf("rec-%04d.%s", artifactSeq.Add(1), format)
	path := filepath.Join(s.media.dir, name)

	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", strconv.Itoa(b.Dx()) + "x" + strconv.Itoa(b.Dy()),
		"-r", "60",
		"-i", "-",
	}

	if format == "gif" {
		// One pass over the frames, generating a palette from them and applying
		// it. A GIF quantised against a palette built from the actual colours
		// looks like the game; the standard 216-colour web palette does not.
		args = append(args,
			"-filter_complex",
			"[0:v] fps=25,split [a][b];[a] palettegen=stats_mode=diff [p];[b][p] paletteuse=dither=bayer:bayer_scale=3")
	} else {
		args = append(args,
			// yuv420p is what every player can decode, and it insists on even
			// dimensions, which a game's logical resolution does not promise.
			"-vf", "pad=ceil(iw/2)*2:ceil(ih/2)*2",
			"-pix_fmt", "yuv420p",
			"-c:v", "libx264",
			"-preset", "veryfast")
	}

	cmd := exec.Command("ffmpeg", append(args, path)...)

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

	art := &Artifact{
		Path:   path,
		Kind:   format,
		Width:  b.Dx(),
		Height: b.Dy(),
		Bytes:  int(info.Size()),
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
		out.Delay = append(out.Delay, 2) // hundredths of a second, so ~50fps
	}

	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, out); err != nil {
		return nil, fmt.Errorf("encoding gif: %w", err)
	}

	return s.media.save("rec", "gif", buf.Bytes(), b.Dx(), b.Dy())
}
