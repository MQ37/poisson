package imaging

import (
	"encoding/binary"
	"image"
)

// jpegOrientation returns the EXIF Orientation tag (1-8, per the TIFF/EXIF
// spec) from a JPEG's APP1 segment, or 0 if data isn't JPEG, carries no EXIF
// segment, or the tag is absent/malformed/out of range. Camera phones
// routinely write portrait photos in landscape sensor order plus this tag
// rather than rotating pixels themselves; ignoring it (as a bare
// image.Decode does — the stdlib has no EXIF support) silently ships the
// photo sideways/upside-down to the vision model, permanently, since the
// re-encoded PNG output has nowhere to carry the tag either.
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 0 // not a JPEG
	}
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return 0 // malformed marker stream
		}
		marker := data[i+1]
		if marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) {
			// Standalone markers (SOI/EOI/RST*) carry no length field.
			i += 2
			continue
		}
		if marker == 0xDA {
			// Start of scan: compressed image data follows: no more
			// markers to find before EOF for our purposes.
			return 0
		}
		segLen := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if segLen < 2 || i+2+segLen > len(data) {
			return 0
		}
		if marker == 0xE1 { // APP1
			if o := parseExifOrientation(data[i+4 : i+2+segLen]); o != 0 {
				return o
			}
		}
		i += 2 + segLen
	}
	return 0
}

// parseExifOrientation reads the Orientation tag (0x0112) out of an APP1
// segment's TIFF-format EXIF payload. seg starts right after the segment's
// own 2-byte length field (i.e. at the "Exif\0\0" marker, if present).
func parseExifOrientation(seg []byte) int {
	if len(seg) < 10 || string(seg[:6]) != "Exif\x00\x00" {
		return 0
	}
	tiff := seg[6:]
	if len(tiff) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	ifdOffset := int(bo.Uint32(tiff[4:8]))
	if ifdOffset < 0 || ifdOffset+2 > len(tiff) {
		return 0
	}
	p := ifdOffset
	count := int(bo.Uint16(tiff[p : p+2]))
	p += 2
	for e := 0; e < count; e++ {
		if p+12 > len(tiff) {
			return 0
		}
		tag := bo.Uint16(tiff[p : p+2])
		if tag == 0x0112 {
			typ := bo.Uint16(tiff[p+2 : p+4])
			if typ != 3 { // SHORT — the only type a real Orientation tag uses
				return 0
			}
			val := int(bo.Uint16(tiff[p+8 : p+10]))
			if val < 1 || val > 8 {
				return 0
			}
			return val
		}
		p += 12
	}
	return 0
}

// applyOrientation corrects src's pixels for EXIF orientation o (as
// returned by jpegOrientation) so the encoded output displays upright with
// no orientation tag of its own needed. o of 0 or 1 (absent or already
// normal) is a no-op returning src unchanged — the overwhelmingly common
// case, so this never allocates or copies pixels for a normal photo.
func applyOrientation(src image.Image, o int) image.Image {
	if o < 2 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	x0, y0 := b.Min.X, b.Min.Y

	swapped := o == 5 || o == 6 || o == 7 || o == 8
	dw, dh := w, h
	if swapped {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))

	for dy := 0; dy < dh; dy++ {
		for dx := 0; dx < dw; dx++ {
			var sx, sy int
			switch o {
			case 2: // mirror horizontal
				sx, sy = w-1-dx, dy
			case 3: // rotate 180
				sx, sy = w-1-dx, h-1-dy
			case 4: // mirror vertical
				sx, sy = dx, h-1-dy
			case 5: // transpose (mirror across the main diagonal)
				sx, sy = dy, dx
			case 6: // rotate 90 CW
				sx, sy = dy, h-1-dx
			case 7: // transverse (mirror across the anti-diagonal)
				sx, sy = w-1-dy, h-1-dx
			case 8: // rotate 90 CCW
				sx, sy = w-1-dy, dx
			}
			dst.Set(dx, dy, src.At(x0+sx, y0+sy))
		}
	}
	return dst
}
