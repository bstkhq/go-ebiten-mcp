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
// ffmpeg is used when it is there, because H.264 keeps the game's own colours
// and costs almost nothing in size. The GIF fallback quantises to 256 colours
// and is noticeably worse, but it needs nothing beyond the standard library,
// and a mediocre video beats a missing one when the point is to show somebody
// what the change looks like.
func (s *Server) saveVideo(frames []*Frame) (*Artifact, error) {
	if len(frames) == 0 {
		return nil, fmt.Errorf("nothing to encode")
	}

	if _, err := exec.LookPath("ffmpeg"); err == nil {
		return s.saveMP4(frames)
	}
	return s.saveGIF(frames)
}

func (s *Server) saveMP4(frames []*Frame) (*Artifact, error) {
	b := frames[0].Image.Bounds()
	name := fmt.Sprintf("rec-%04d.mp4", artifactSeq.Add(1))
	path := filepath.Join(s.media.dir, name)

	cmd := exec.Command("ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", strconv.Itoa(b.Dx())+"x"+strconv.Itoa(b.Dy()),
		"-r", "60",
		"-i", "-",
		// yuv420p is what every player can decode, and it insists on even
		// dimensions, which a game's logical resolution does not promise.
		"-vf", "pad=ceil(iw/2)*2:ceil(ih/2)*2",
		"-pix_fmt", "yuv420p",
		"-c:v", "libx264",
		"-preset", "veryfast",
		path,
	)

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
		Kind:   "mp4",
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
