package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"poisson/internal/testutil"
)

// makeImage builds a w×h PNG (or JPEG) with a simple gradient so decoders have
// real content to work with.
func makeImage(t *testing.T, w, h int, format string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 128, 255})
		}
	}
	var buf bytes.Buffer
	var err error
	if format == "jpeg" {
		err = jpeg.Encode(&buf, img, nil)
	} else {
		err = png.Encode(&buf, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func dims(t *testing.T, path string) (int, int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return cfg.Width, cfg.Height
}

func TestProcessDownscalesLongEdge(t *testing.T) {
	t.Setenv("TMPDIR", testutil.TempDir(t))
	data := makeImage(t, 2000, 1000, "png")
	path, mt, err := Process(data)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })
	if mt != "image/png" {
		t.Errorf("media type = %q", mt)
	}
	if !strings.HasSuffix(path, ".png") {
		t.Errorf("path %q not png", path)
	}
	w, h := dims(t, path)
	if w != MaxLongEdge {
		t.Errorf("width = %d, want %d (long edge capped)", w, MaxLongEdge)
	}
	if h != MaxLongEdge/2 {
		t.Errorf("height = %d, want %d (aspect preserved)", h, MaxLongEdge/2)
	}
}

func TestProcessNoUpscale(t *testing.T) {
	t.Setenv("TMPDIR", testutil.TempDir(t))
	data := makeImage(t, 500, 400, "png")
	path, _, err := Process(data)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })
	w, h := dims(t, path)
	if w != 500 || h != 400 {
		t.Errorf("dims = %dx%d, want 500x400 (unchanged)", w, h)
	}
}

func TestProcessJPEGtoPNG(t *testing.T) {
	t.Setenv("TMPDIR", testutil.TempDir(t))
	data := makeImage(t, 1600, 1600, "jpeg")
	path, mt, err := Process(data)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })
	if mt != "image/png" {
		t.Errorf("media type = %q, want image/png", mt)
	}
	w, h := dims(t, path)
	if w != MaxLongEdge || h != MaxLongEdge {
		t.Errorf("dims = %dx%d, want %d square", w, h, MaxLongEdge)
	}
}

func TestProcessFile(t *testing.T) {
	dir := testutil.TempDir(t)
	src := filepath.Join(dir, "in.png")
	if err := os.WriteFile(src, makeImage(t, 1200, 600, "png"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, _, err := ProcessFile(src)
	if err != nil {
		t.Fatalf("ProcessFile: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })
	if _, err := os.Stat(path); err != nil {
		t.Errorf("temp file missing: %v", err)
	}
	w, _ := dims(t, path)
	if w != MaxLongEdge {
		t.Errorf("width = %d, want %d", w, MaxLongEdge)
	}
}

func TestProcessRejectsGarbage(t *testing.T) {
	t.Setenv("TMPDIR", testutil.TempDir(t))
	if _, _, err := Process([]byte("not an image")); err == nil {
		t.Error("expected decode error for non-image bytes")
	}
}
