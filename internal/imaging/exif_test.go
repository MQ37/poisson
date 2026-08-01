package imaging

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"testing"

	"github.com/mq37/poisson/internal/testutil"
)

// buildAPP1 builds a minimal well-formed APP1 EXIF segment (everything
// after the 2-byte marker+length prefix jpegOrientation itself strips off
// before calling parseExifOrientation) carrying a single Orientation
// (0x0112) IFD0 entry with the given value. bigEndian selects "MM" (Motorola)
// vs "II" (Intel) TIFF byte order — real cameras use either.
func buildAPP1(t *testing.T, orientation int, bigEndian bool) []byte {
	t.Helper()
	var bo binary.ByteOrder = binary.LittleEndian
	boMarker := "II"
	if bigEndian {
		bo = binary.BigEndian
		boMarker = "MM"
	}
	var buf bytes.Buffer
	buf.WriteString("Exif\x00\x00")
	buf.WriteString(boMarker)
	u16 := make([]byte, 2)
	u32 := make([]byte, 4)
	bo.PutUint16(u16, 42)
	buf.Write(u16)
	bo.PutUint32(u32, 8) // IFD0 starts right after this 8-byte TIFF header
	buf.Write(u32)
	bo.PutUint16(u16, 1) // 1 IFD entry
	buf.Write(u16)
	bo.PutUint16(u16, 0x0112) // Orientation tag
	buf.Write(u16)
	bo.PutUint16(u16, 3) // type SHORT
	buf.Write(u16)
	bo.PutUint32(u32, 1) // count 1
	buf.Write(u32)
	bo.PutUint16(u16, uint16(orientation)) // value, inline (fits in 4 bytes)
	buf.Write(u16)
	buf.Write([]byte{0, 0}) // pad the 4-byte value/offset slot
	return buf.Bytes()
}

// wrapAsAPP1Segment prepends the 0xFFE1 marker and 2-byte big-endian length
// (which includes the length field itself, per the JPEG marker-segment
// format) that jpegOrientation expects to walk over.
func wrapAsAPP1Segment(payload []byte) []byte {
	seg := make([]byte, 4+len(payload))
	seg[0], seg[1] = 0xFF, 0xE1
	binary.BigEndian.PutUint16(seg[2:4], uint16(len(payload)+2))
	copy(seg[4:], payload)
	return seg
}

func TestJpegOrientationParsesBothByteOrders(t *testing.T) {
	for _, be := range []bool{false, true} {
		app1 := wrapAsAPP1Segment(buildAPP1(t, 6, be))
		data := append([]byte{0xFF, 0xD8}, app1...) // SOI + APP1, no scan data needed
		got := jpegOrientation(data)
		if got != 6 {
			t.Errorf("bigEndian=%v: jpegOrientation = %d, want 6", be, got)
		}
	}
}

func TestJpegOrientationNonJPEGReturnsZero(t *testing.T) {
	if got := jpegOrientation([]byte("not a jpeg at all")); got != 0 {
		t.Errorf("jpegOrientation(non-JPEG) = %d, want 0", got)
	}
	if got := jpegOrientation(nil); got != 0 {
		t.Errorf("jpegOrientation(nil) = %d, want 0", got)
	}
}

func TestJpegOrientationNoExifReturnsZero(t *testing.T) {
	data := makeImage(t, 4, 4, "jpeg") // real JPEG, no APP1/EXIF at all
	if got := jpegOrientation(data); got != 0 {
		t.Errorf("jpegOrientation(no EXIF) = %d, want 0", got)
	}
}

func TestJpegOrientationOutOfRangeIgnored(t *testing.T) {
	app1 := wrapAsAPP1Segment(buildAPP1(t, 99, false))
	data := append([]byte{0xFF, 0xD8}, app1...)
	if got := jpegOrientation(data); got != 0 {
		t.Errorf("jpegOrientation(out-of-range value) = %d, want 0", got)
	}
}

// TestApplyOrientationEachCase verifies all 8 EXIF orientation codes against
// a 2x3 (w=2,h=3) source image where every pixel is a distinct color, so a
// mis-derived coordinate mapping shows up as the wrong color at a probed
// point rather than an accidentally-still-plausible-looking image.
func TestApplyOrientationEachCase(t *testing.T) {
	w, h := 2, 3
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	colorAt := func(x, y int) color.RGBA { return color.RGBA{uint8(x*100 + 10), uint8(y*50 + 10), 200, 255} }
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			src.Set(x, y, colorAt(x, y))
		}
	}

	cases := []struct {
		o          int
		wantW      int
		wantH      int
		probeDX    int
		probeDY    int
		wantSrcX   int
		wantSrcY   int
	}{
		{1, w, h, 0, 0, 0, 0},          // normal: no-op (applyOrientation returns src unchanged, not even a copy)
		{2, w, h, 0, 0, w - 1, 0},      // mirror horizontal: dst(0,0) = src(w-1,0)
		{3, w, h, 0, 0, w - 1, h - 1},  // rotate 180: dst(0,0) = src(w-1,h-1)
		{4, w, h, 0, 0, 0, h - 1},      // mirror vertical: dst(0,0) = src(0,h-1)
		{5, h, w, 0, 0, 0, 0},          // transpose: dst(0,0) = src(0,0)
		{6, h, w, h - 1, 0, 0, 0},      // rotate 90 CW: dst(h-1,0) = src(0,0)
		{7, h, w, 0, 0, w - 1, h - 1},  // transverse: dst(0,0) = src(w-1,h-1)
		{8, h, w, 0, w - 1, 0, 0},      // rotate 90 CCW: dst(0,w-1) = src(0,0)
	}
	for _, c := range cases {
		out := applyOrientation(src, c.o)
		b := out.Bounds()
		if b.Dx() != c.wantW || b.Dy() != c.wantH {
			t.Errorf("o=%d: dims = %dx%d, want %dx%d", c.o, b.Dx(), b.Dy(), c.wantW, c.wantH)
			continue
		}
		got := out.At(c.probeDX, c.probeDY)
		want := colorAt(c.wantSrcX, c.wantSrcY)
		gr, gg, gb, _ := got.RGBA()
		wr, wg, wb, _ := want.RGBA()
		if gr != wr || gg != wg || gb != wb {
			t.Errorf("o=%d: pixel at dst(%d,%d) = %v, want src(%d,%d) = %v", c.o, c.probeDX, c.probeDY, got, c.wantSrcX, c.wantSrcY, want)
		}
	}
}

func TestApplyOrientationInvalidIsNoOp(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 3))
	for _, o := range []int{0, 1, 9, -1} {
		out := applyOrientation(src, o)
		if out != image.Image(src) {
			t.Errorf("o=%d: expected the exact same image returned unchanged", o)
		}
	}
}

// TestProcessCorrectsRotatedJPEG is the end-to-end regression test: a real
// decodable JPEG with an EXIF Orientation=6 (rotate 90 CW) tag, run through
// the actual Process() entry point, must come out upright in the final PNG
// — before this fix, image.Decode's lack of EXIF support meant the sensor-
// order pixels went straight through unrotated.
func TestProcessCorrectsRotatedJPEG(t *testing.T) {
	t.Setenv("TMPDIR", testutil.TempDir(t))

	// Left half (sensor-order x < 20) solid red, right half solid black —
	// large, block-aligned regions so JPEG's lossy 8x8-DCT compression
	// doesn't blur a single marker pixel below recognition (verified this
	// was exactly the problem with a 1px marker: block-compression bled it
	// into the surrounding black before rotation was even involved).
	const w, h = 40, 20
	base := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{0, 0, 0, 255}
			if x < w/2 {
				c = color.RGBA{255, 0, 0, 255}
			}
			base.Set(x, y, c)
		}
	}
	var rawBuf bytes.Buffer
	if err := jpeg.Encode(&rawBuf, base, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode base jpeg: %v", err)
	}
	raw := rawBuf.Bytes()

	app1 := wrapAsAPP1Segment(buildAPP1(t, 6, false)) // rotate 90 CW
	jpegData := append(append([]byte{0xFF, 0xD8}, app1...), raw[2:]...)

	path, mediaType, err := Process(jpegData)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })
	if mediaType != "image/png" {
		t.Fatalf("mediaType = %q", mediaType)
	}

	f, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	out, err := png.Decode(bytes.NewReader(f))
	if err != nil {
		t.Fatalf("decode result png: %v", err)
	}
	b := out.Bounds()
	// Rotate 90 CW maps dst(dx,dy) = src(dy, h-1-dx) (see
	// TestApplyOrientationEachCase's o=6 case): dst's dy coordinate
	// directly addresses src's x-axis, so the red left-half (src x<20)
	// ends up at dst dy<20, and the black right-half at dst dy>=20 —
	// regardless of dx. A 40x20 source becomes 20x40 (dims swapped).
	if b.Dx() >= w {
		t.Fatalf("output not rotated: bounds = %v, want width < %d", b, w)
	}
	isRed := func(x, y int) bool {
		r, g, bl, _ := out.At(x, y).RGBA()
		return r > 0xA000 && g < 0x3000 && bl < 0x3000
	}
	isBlack := func(x, y int) bool {
		r, g, bl, _ := out.At(x, y).RGBA()
		return r < 0x3000 && g < 0x3000 && bl < 0x3000
	}
	// Sample well inside each half, away from the compression-bled boundary.
	if !isRed(b.Dx()/2, 5) {
		r, g, bl, _ := out.At(b.Dx()/2, 5).RGBA()
		t.Errorf("dst(dy=5) expected red (from src's left half), got rgba=(%d,%d,%d)", r, g, bl)
	}
	if !isBlack(b.Dx()/2, 25) {
		r, g, bl, _ := out.At(b.Dx()/2, 25).RGBA()
		t.Errorf("dst(dy=25) expected black (from src's right half), got rgba=(%d,%d,%d)", r, g, bl)
	}
}
