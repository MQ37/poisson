// Package imaging decodes, downscales, and re-encodes user-supplied images so
// they cost fewer vision tokens. Images are capped at MaxLongEdge on the longer
// side (downscale only, aspect preserved), re-encoded as PNG, and written to a
// temp file the providers read at request-build time.
package imaging

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"

	"golang.org/x/image/draw"

	// Register decoders. PNG/JPEG/GIF are stdlib; WebP comes from x/image.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"
)

// MaxLongEdge caps the longer dimension of a processed image. 1024px keeps
// screenshots/code legible at ~800–1000 vision tokens while staying under every
// provider's own downscale threshold (predictable cost).
const MaxLongEdge = 1024

// Process decodes image bytes, downscales so the long edge is at most
// MaxLongEdge, re-encodes as PNG, and writes the result to a temp file. It
// returns the file path and its media type ("image/png").
func Process(data []byte) (path, mediaType string, err error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", "", fmt.Errorf("decode image: %w", err)
	}
	out := downscale(src)

	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return "", "", fmt.Errorf("encode png: %w", err)
	}

	f, err := os.CreateTemp("", "poisson-img-*.png")
	if err != nil {
		return "", "", fmt.Errorf("create temp: %w", err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", "", fmt.Errorf("write temp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", "", fmt.Errorf("close temp: %w", err)
	}
	return f.Name(), "image/png", nil
}

// ProcessFile is Process for a file on disk.
func ProcessFile(srcPath string) (path, mediaType string, err error) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", srcPath, err)
	}
	return Process(data)
}

// downscale returns src scaled so its long edge is at most MaxLongEdge. Images
// already within the cap are returned unchanged (never upscaled).
func downscale(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	long := w
	if h > long {
		long = h
	}
	if long <= MaxLongEdge || long == 0 {
		return src
	}
	nw := w * MaxLongEdge / long
	nh := h * MaxLongEdge / long
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}
