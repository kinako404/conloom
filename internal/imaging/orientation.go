package imaging

import (
	"bytes"
	"encoding/binary"
	"image"
)

// exifOrientation returns the EXIF Orientation (1–8) of the JPEG data: the
// tag in IFD0 of the first APP1 Exif segment before the image data. It is 1
// when data is not a JPEG, has no such tag, or the tag or its segment is
// malformed or out of range.
func exifOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 1
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xff {
			return 1
		}
		marker := data[i+1]
		switch {
		case marker == 0xff:
			i++
			continue
		case marker == 0xd8 || marker >= 0xd0 && marker <= 0xd7 || marker == 0x01:
			i += 2
			continue
		case marker == 0xda || marker == 0xd9:
			return 1
		}
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		if n < 2 || i+2+n > len(data) {
			return 1
		}
		seg := data[i+4 : i+2+n]
		if marker == 0xe1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
			return tiffOrientation(seg[6:])
		}
		i += 2 + n
	}
	return 1
}

// tiffOrientation returns the Orientation tag of IFD0 of the TIFF block b, 1
// when it has none or b is malformed.
func tiffOrientation(b []byte) int {
	if len(b) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(b[:4]) {
	case "II*\x00":
		order = binary.LittleEndian
	case "MM\x00*":
		order = binary.BigEndian
	default:
		return 1
	}
	ifd := order.Uint32(b[4:])
	if ifd > uint32(len(b)-2) {
		return 1
	}
	count := int(order.Uint16(b[ifd:]))
	entries := b[ifd+2:]
	for k := 0; k < count && 12*k+12 <= len(entries); k++ {
		e := entries[12*k : 12*k+12]
		if order.Uint16(e) != 0x0112 {
			continue
		}
		if order.Uint16(e[2:]) != 3 || order.Uint32(e[4:]) != 1 {
			return 1
		}
		if o := int(order.Uint16(e[8:])); o >= 1 && o <= 8 {
			return o
		}
		return 1
	}
	return 1
}

// orient returns img turned upright for EXIF orientation o: flipped and/or
// rotated, with width and height swapped for 5–8; img itself for 1 or an
// unknown value. img must start at the origin.
func orient(img *image.RGBA, o int) *image.RGBA {
	if o < 2 || o > 8 {
		return img
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		for x := range dw {
			var sx, sy int
			switch o {
			case 2:
				sx, sy = w-1-x, y
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sx, sy = x, h-1-y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, h-1-x
			case 7:
				sx, sy = w-1-y, h-1-x
			case 8:
				sx, sy = w-1-y, x
			}
			copy(dst.Pix[y*dst.Stride+4*x:y*dst.Stride+4*x+4], img.Pix[sy*img.Stride+4*sx:])
		}
	}
	return dst
}
