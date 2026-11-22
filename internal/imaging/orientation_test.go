package imaging

import (
	"encoding/binary"
	"image"
	"image/color"
	"testing"
)

// exifSegment is a JPEG APP1 segment holding an EXIF block whose IFD0 has
// the Orientation tag set to orientation, in big-endian (MM) or little-endian
// (II) byte order, after an unrelated tag.
func exifSegment(orientation uint16, bigEndian bool) []byte {
	var order binary.AppendByteOrder = binary.LittleEndian
	tiff := []byte("II*\x00")
	if bigEndian {
		order, tiff = binary.BigEndian, []byte("MM\x00*")
	}
	tiff = order.AppendUint32(tiff, 8)
	tiff = order.AppendUint16(tiff, 2)
	// ImageDescription (ASCII, 3 bytes in the value field), then Orientation.
	tiff = order.AppendUint16(tiff, 0x010e)
	tiff = order.AppendUint16(tiff, 2)
	tiff = order.AppendUint32(tiff, 3)
	tiff = append(tiff, 'a', 'b', 0, 0)
	tiff = order.AppendUint16(tiff, 0x0112)
	tiff = order.AppendUint16(tiff, 3)
	tiff = order.AppendUint32(tiff, 1)
	tiff = order.AppendUint16(tiff, orientation)
	tiff = append(tiff, 0, 0)
	tiff = order.AppendUint32(tiff, 0)
	body := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xff, 0xe1}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(body)+2))
	return append(seg, body...)
}

// withSegments inserts segs into the JPEG data right after its SOI marker.
func withSegments(data []byte, segs ...[]byte) []byte {
	out := append([]byte{}, data[:2]...)
	for _, s := range segs {
		out = append(out, s...)
	}
	return append(out, data[2:]...)
}

var (
	quadTL = color.RGBA{220, 30, 30, 255}
	quadTR = color.RGBA{30, 200, 30, 255}
	quadBL = color.RGBA{30, 30, 220, 255}
	quadBR = color.RGBA{230, 230, 40, 255}
)

// quadrant is the colour of the upright w×h test picture at (x, y): one
// colour per quadrant.
func quadrant(x, y, w, h int) color.RGBA {
	switch {
	case y < h/2 && x < w/2:
		return quadTL
	case y < h/2:
		return quadTR
	case x < w/2:
		return quadBL
	}
	return quadBR
}

// storedAs returns the pixels a camera stores for the upright w×h quadrant
// picture with EXIF orientation o, from the tag's definition of where stored
// row 0 and column 0 are in the upright picture: 1 top/left, 2 top/right,
// 3 bottom/right, 4 bottom/left, 5 left/top, 6 right/top, 7 right/bottom,
// 8 left/bottom.
func storedAs(o, w, h int) *image.RGBA {
	sw, sh := w, h
	if o >= 5 {
		sw, sh = h, w
	}
	img := image.NewRGBA(image.Rect(0, 0, sw, sh))
	for r := range sh {
		for c := range sw {
			var x, y int
			switch o {
			case 1:
				x, y = c, r
			case 2:
				x, y = w-1-c, r
			case 3:
				x, y = w-1-c, h-1-r
			case 4:
				x, y = c, h-1-r
			case 5:
				x, y = r, c
			case 6:
				x, y = w-1-r, c
			case 7:
				x, y = w-1-r, h-1-c
			case 8:
				x, y = r, h-1-c
			}
			img.SetRGBA(c, r, quadrant(x, y, w, h))
		}
	}
	return img
}

// checkUpright checks that img is the upright w×h quadrant picture.
func checkUpright(t *testing.T, what string, img image.Image, w, h int) {
	t.Helper()
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Errorf("%s is %d×%d, want %d×%d", what, b.Dx(), b.Dy(), w, h)
		return
	}
	for _, p := range []image.Point{{w / 4, h / 4}, {3 * w / 4, h / 4}, {w / 4, 3 * h / 4}, {3 * w / 4, 3 * h / 4}} {
		if got, want := img.At(p.X, p.Y), quadrant(p.X, p.Y, w, h); !near(got, want, 40) {
			t.Errorf("%s at %v = %v, want %v", what, p, got, want)
		}
	}
}

func TestProcessChat_AppliesEXIFOrientation(t *testing.T) {
	const w, h = 96, 64
	for o := 1; o <= 8; o++ {
		for _, big := range []bool{false, true} {
			data := withSegments(jpegOf(t, storedAs(o, w, h)), exifSegment(uint16(o), big))
			c := processChat(t, data)
			if c.ViewWidth != w || c.ViewHeight != h || c.ReplyWidth != w || c.ReplyHeight != h {
				t.Errorf("orientation %d (big endian %v): view %d×%d, reply %d×%d; want %d×%d", o, big,
					c.ViewWidth, c.ViewHeight, c.ReplyWidth, c.ReplyHeight, w, h)
			}
			checkUpright(t, "view copy", decodeJPEG(t, c.ViewJPEG), w, h)
			checkUpright(t, "reply copy", decodeJPEG(t, c.ReplyJPEG), w, h)
		}
	}
}

// A portrait photo stored sideways (orientation 6) larger than the view edge
// comes out upright at the view and reply sizes.
func TestProcessChat_RotatesBeforeShrinking(t *testing.T) {
	const w, h = 1600, 3200
	c := processChat(t, withSegments(jpegOf(t, storedAs(6, w, h)), exifSegment(6, true)))
	if c.ViewWidth != 768 || c.ViewHeight != 1536 || c.ReplyWidth != 384 || c.ReplyHeight != 768 {
		t.Fatalf("view %d×%d, reply %d×%d; want 768×1536 and 384×768", c.ViewWidth, c.ViewHeight, c.ReplyWidth, c.ReplyHeight)
	}
	checkUpright(t, "view copy", decodeJPEG(t, c.ViewJPEG), 768, 1536)
}

// The persona path decodes the same way: its reply copy and dimensions are
// upright too.
func TestProcess_AppliesEXIFOrientation(t *testing.T) {
	const w, h = 96, 64
	p := process(t, withSegments(jpegOf(t, storedAs(8, w, h)), exifSegment(8, false)))
	if p.Width != w || p.Height != h {
		t.Errorf("size = %d×%d, want %d×%d", p.Width, p.Height, w, h)
	}
	checkUpright(t, "reply copy", decodeJPEG(t, p.ReplyJPEG), w, h)
}

func TestEXIFOrientation_Malformed(t *testing.T) {
	plain := jpegOf(t, gradient(16, 16))
	good := exifSegment(6, true)
	truncated := append([]byte{}, good...)
	binary.BigEndian.PutUint16(truncated[2:], uint16(len(good)+40))
	badOffset := append([]byte{}, good...)
	binary.BigEndian.PutUint32(badOffset[4+6+4:], 0xfffffff0)
	for _, c := range []struct {
		name string
		data []byte
		want int
	}{
		{"no EXIF", plain, 1},
		{"orientation 6", withSegments(plain, good), 6},
		{"orientation 0", withSegments(plain, exifSegment(0, false)), 1},
		{"orientation 9", withSegments(plain, exifSegment(9, false)), 1},
		{"after another APP segment", withSegments(plain, []byte{0xff, 0xe0, 0, 4, 'x', 'y'}, good), 6},
		{"segment runs past the data", append([]byte{0xff, 0xd8}, truncated...), 1},
		{"IFD offset past the block", withSegments(plain, badOffset), 1},
		{"not EXIF", withSegments(plain, []byte{0xff, 0xe1, 0, 8, 'h', 't', 't', 'p', ':', '/'}), 1},
		{"empty", nil, 1},
		{"PNG", pngOf(t, gradient(4, 4)), 1},
	} {
		if got := exifOrientation(c.data); got != c.want {
			t.Errorf("%s: orientation = %d, want %d", c.name, got, c.want)
		}
	}
	// A JPEG whose EXIF block is broken still decodes, unrotated.
	broken := withSegments(jpegOf(t, gradient(40, 20)), badOffset)
	if c := processChat(t, broken); c.ViewWidth != 40 || c.ViewHeight != 20 {
		t.Errorf("broken EXIF: view %d×%d, want 40×20", c.ViewWidth, c.ViewHeight)
	}
}
