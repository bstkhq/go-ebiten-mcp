package ebitenmcp

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sync/atomic"

	"golang.org/x/image/draw"
)

// MediaDir is where artifacts are written, relative to the game's working
// directory. Add it to .gitignore.
const MediaDir = ".ebitenmcp/media"

// inlineMaxSize caps the longest side of the image returned inline in a tool
// result.
//
// Anything captured is also written to disk at full resolution, so nothing is
// lost by scaling the inline copy. What would be lost by not scaling it is the
// conversation: a 1080p frame costs a large slice of the context window every
// time it is looked at, and most of those pixels answer nothing.
const inlineMaxSize = 1024

// Artifact is one produced file, in the three forms it is needed in: on disk
// for a person to open, over HTTP for a client that can render it, and inline
// in the tool result for the agent and for the chat.
type Artifact struct {
	Path   string `json:"path"`
	URL    string `json:"url,omitempty"`
	Kind   string `json:"kind"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`

	// Frames and FPS describe a recording, and are left out of everything else.
	//
	// They are here because the answer would otherwise not say, and the two
	// things that write a gif do not agree: with ffmpeg the frames are
	// resampled and roughly half of them are dropped, without it every one is
	// kept and played twice as fast. Same tool, same arguments, different
	// machine. A caller comparing two recordings, or recording frames in order
	// to step through them, needs to know which of those it got.
	Frames int     `json:"frames,omitempty"`
	FPS    float64 `json:"fps,omitempty"`
}

var artifactSeq atomic.Uint64

// media writes artifacts and knows how to address them over HTTP.
type media struct {
	dir     string
	baseURL string
}

func newMedia(dir, baseURL string) (*media, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", dir, err)
	}
	return &media{dir: dir, baseURL: baseURL}, nil
}

// save writes bytes under a generated name and describes where they landed.
func (m *media) save(prefix, ext string, data []byte, width, height int) (*Artifact, error) {
	name := fmt.Sprintf("%s-%04d.%s", prefix, artifactSeq.Add(1), ext)
	path := filepath.Join(m.dir, name)

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}

	art := &Artifact{
		Path:   path,
		Kind:   ext,
		Width:  width,
		Height: height,
		Bytes:  len(data),
	}
	if m.baseURL != "" {
		art.URL = m.baseURL + "/media/" + name
	}
	return art, nil
}

// savePNG writes an image losslessly, at whatever size it already is.
func (m *media) savePNG(prefix string, img image.Image) (*Artifact, error) {
	data, err := encodePNG(img)
	if err != nil {
		return nil, err
	}

	b := img.Bounds()
	return m.save(prefix, "png", data, b.Dx(), b.Dy())
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encoding png: %w", err)
	}
	return buf.Bytes(), nil
}

// fitInline scales an image down to fit within max on its longest side. Images
// already small enough are returned untouched, so the common case of a game at
// a modest resolution stays pixel-exact.
func fitInline(img image.Image, max int) image.Image {
	if max <= 0 {
		return img
	}

	b := img.Bounds()
	longest := b.Dx()
	if b.Dy() > longest {
		longest = b.Dy()
	}
	if longest <= max {
		return img
	}

	scale := float64(max) / float64(longest)
	w := int(float64(b.Dx()) * scale)
	h := int(float64(b.Dy()) * scale)

	// CatmullRom rather than a box filter: these images are read by eye and by
	// a model, and legibility of small text in a HUD is the thing that matters.
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(out, out.Bounds(), img, b, draw.Over, nil)
	return out
}
