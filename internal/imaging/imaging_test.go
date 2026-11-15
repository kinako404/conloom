package imaging

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"runtime"
	"testing"
)

func pngOf(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jpegOf(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodeJPEG(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode JPEG: %v", err)
	}
	return img
}

func process(t *testing.T, data []byte) Processed {
	t.Helper()
	p, err := Process(data)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	return p
}

// gradient is an opaque w×h image whose red grows from left to right and
// green from top to bottom.
func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{uint8(x * 255 / (w - 1)), uint8(y * 255 / (h - 1)), 128, 255})
		}
	}
	return img
}

func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// near reports whether c is within tol of want in each of red, green and blue.
func near(c color.Color, want color.RGBA, tol int) bool {
	r, g, b, _ := c.RGBA()
	return absDiff(int(r>>8), int(want.R)) <= tol && absDiff(int(g>>8), int(want.G)) <= tol &&
		absDiff(int(b>>8), int(want.B)) <= tol
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

func writeChunk(buf *bytes.Buffer, typ string, data []byte) {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(data)))
	buf.Write(n[:])
	buf.WriteString(typ)
	buf.Write(data)
	crc := crc32.NewIEEE()
	crc.Write([]byte(typ))
	crc.Write(data)
	binary.BigEndian.PutUint32(n[:], crc.Sum32())
	buf.Write(n[:])
}

// pngHeader is a PNG claiming an 8-bit grayscale w×h image whose pixel data
// stops after the first row: its header is valid, decoding it fails.
func pngHeader(t *testing.T, w, h uint32) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr, w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8] = 8
	writeChunk(&buf, "IHDR", ihdr)
	var idat bytes.Buffer
	zw := zlib.NewWriter(&idat)
	if _, err := zw.Write(make([]byte, 1+int(w))); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	writeChunk(&buf, "IDAT", idat.Bytes())
	writeChunk(&buf, "IEND", nil)
	return buf.Bytes()
}

// jpegWithSize is a small valid JPEG whose frame header is changed to claim
// w×h.
func jpegWithSize(t *testing.T, w, h uint16) []byte {
	t.Helper()
	data := jpegOf(t, gradient(16, 16))
	i := bytes.Index(data, []byte{0xff, 0xc0})
	if i < 0 {
		t.Fatal("no SOF0 marker")
	}
	binary.BigEndian.PutUint16(data[i+5:], h)
	binary.BigEndian.PutUint16(data[i+7:], w)
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != int(w) || cfg.Height != int(h) {
		t.Fatalf("crafted JPEG config = %+v, %v; want %d×%d", cfg, err, w, h)
	}
	return data
}

func TestImaging_PNGAndJPEG(t *testing.T) {
	for _, tc := range []struct {
		name           string
		w, h           int
		jpeg           bool
		replyW, replyH int
		tokens         int
	}{
		{"landscape png", 1000, 500, false, 768, 384, 394},
		{"portrait jpeg", 600, 1200, true, 384, 768, 394},
		{"odd ratio png", 1001, 333, false, 768, 255, 282},
		{"square jpeg at the reply edge", 768, 768, true, 768, 768, 786},
		{"small png kept", 300, 200, false, 300, 200, 90},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := gradient(tc.w, tc.h)
			data, mime := pngOf(t, src), "image/png"
			if tc.jpeg {
				data, mime = jpegOf(t, src), "image/jpeg"
			}
			p := process(t, data)
			if p.MIME != mime || p.Width != tc.w || p.Height != tc.h {
				t.Fatalf("MIME, size = %s, %d×%d; want %s, %d×%d", p.MIME, p.Width, p.Height, mime, tc.w, tc.h)
			}
			if !bytes.Equal(p.Original, data) {
				t.Fatal("Original differs from the uploaded bytes")
			}
			if p.ReplyWidth != tc.replyW || p.ReplyHeight != tc.replyH || p.ReplyTokens != tc.tokens {
				t.Fatalf("reply = %d×%d, %d tokens; want %d×%d, %d tokens",
					p.ReplyWidth, p.ReplyHeight, p.ReplyTokens, tc.replyW, tc.replyH, tc.tokens)
			}
			reply := decodeJPEG(t, p.ReplyJPEG)
			if b := reply.Bounds(); b.Dx() != tc.replyW || b.Dy() != tc.replyH {
				t.Fatalf("reply JPEG is %v, want %d×%d", b, tc.replyW, tc.replyH)
			}
			for _, f := range [][2]float64{{0.25, 0.25}, {0.75, 0.5}, {0.5, 0.75}} {
				want := src.RGBAAt(int(f[0]*float64(tc.w)), int(f[1]*float64(tc.h)))
				got := reply.At(int(f[0]*float64(tc.replyW)), int(f[1]*float64(tc.replyH)))
				if !near(got, want, 12) {
					t.Errorf("reply pixel at %v = %v, want about %v", f, got, want)
				}
			}
		})
	}
}

func TestImaging_Tokens(t *testing.T) {
	for _, tc := range []struct{ w, h, want int }{
		{1, 1, 3}, {28, 28, 3}, {29, 28, 4}, {56, 57, 8}, {768, 384, 394}, {768, 768, 786},
	} {
		if got := Tokens(tc.w, tc.h); got != tc.want {
			t.Errorf("Tokens(%d, %d) = %d, want %d", tc.w, tc.h, got, tc.want)
		}
	}
}

func TestImaging_DownscaleAveragesArea(t *testing.T) {
	checker := image.NewGray(image.Rect(0, 0, 1536, 1536))
	for y := range 1536 {
		for x := range 1536 {
			if (x+y)%2 == 0 {
				checker.SetGray(x, y, color.Gray{Y: 255})
			}
		}
	}
	corners := func(w, h int) []image.Point {
		return []image.Point{{0, 0}, {w - 1, 0}, {0, h - 1}, {w - 1, h - 1}, {w / 2, h / 2}}
	}
	reply := decodeJPEG(t, process(t, pngOf(t, checker)).ReplyJPEG)
	for _, pt := range corners(768, 768) {
		if got := reply.At(pt.X, pt.Y); !near(got, color.RGBA{128, 128, 128, 255}, 6) {
			t.Errorf("halved checkerboard at %v = %v, want mid gray", pt, got)
		}
	}

	orange := color.RGBA{200, 100, 50, 255}
	reply = decodeJPEG(t, process(t, pngOf(t, solid(1000, 1000, orange))).ReplyJPEG)
	for _, pt := range corners(768, 768) {
		if got := reply.At(pt.X, pt.Y); !near(got, orange, 6) {
			t.Errorf("shrunk solid image at %v = %v, want %v", pt, got, orange)
		}
	}
}

func TestImaging_FlattensAlpha(t *testing.T) {
	nrgba := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	pal := image.NewPaletted(image.Rect(0, 0, 100, 100),
		color.Palette{color.NRGBA{0, 0, 255, 0}, color.NRGBA{0, 0, 255, 255}})
	for y := range 100 {
		for x := range 100 {
			if x < 50 {
				nrgba.SetNRGBA(x, y, color.NRGBA{255, 0, 0, 0})
			} else {
				nrgba.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 128})
				pal.SetColorIndex(x, y, 1)
			}
		}
	}
	for _, tc := range []struct {
		name  string
		img   image.Image
		right color.RGBA
	}{
		{"nrgba", nrgba, color.RGBA{127, 127, 127, 255}},
		{"paletted", pal, color.RGBA{0, 0, 255, 255}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := decodeJPEG(t, process(t, pngOf(t, tc.img)).ReplyJPEG)
			if got := reply.At(20, 50); !near(got, color.RGBA{255, 255, 255, 255}, 4) {
				t.Errorf("transparent pixel = %v, want white", got)
			}
			if got := reply.At(80, 50); !near(got, tc.right, 8) {
				t.Errorf("opaque or half transparent pixel = %v, want about %v", got, tc.right)
			}
		})
	}
}

func TestImaging_RejectsDecompressionBomb(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"png 20000×20000", pngHeader(t, 20000, 20000)},
		{"png too wide", pngHeader(t, 8001, 1)},
		{"png too tall", pngHeader(t, 1, 8001)},
		{"png too many pixels", pngHeader(t, 6000, 5000)},
		{"jpeg 20000×20000", jpegWithSize(t, 20000, 20000)},
		{"body over 8 MiB", append(pngOf(t, gradient(16, 16)), make([]byte, MaxBytes)...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			_, err := Process(tc.data)
			runtime.ReadMemStats(&after)
			if !errors.Is(err, ErrTooLarge) {
				t.Fatalf("Process err = %v, want ErrTooLarge", err)
			}
			if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 1<<20 {
				t.Errorf("Process allocated %d bytes, want the pixels left undecoded", alloc)
			}
			if _, err := Scaled(tc.data, 1536); !errors.Is(err, ErrTooLarge) {
				t.Fatalf("Scaled err = %v, want ErrTooLarge", err)
			}
		})
	}
	for _, size := range [][2]uint32{{8000, 3125}, {3125, 8000}} {
		if _, err := Process(pngHeader(t, size[0], size[1])); !errors.Is(err, ErrUnsupported) {
			t.Errorf("Process of a truncated %d×%d PNG err = %v, want ErrUnsupported from decoding", size[0], size[1], err)
		}
	}
}

func TestImaging_SniffsNotHeader(t *testing.T) {
	var gifData bytes.Buffer
	if err := gif.Encode(&gifData, gradient(16, 16), nil); err != nil {
		t.Fatal(err)
	}
	pngData := pngOf(t, gradient(16, 16))
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"gif", gifData.Bytes()},
		{"text", []byte("这不是图片，只是一段文字")},
		{"html", []byte("<html><body>小明</body></html>")},
		{"webp", []byte("RIFF\x24\x00\x00\x00WEBPVP8 ")},
		{"empty", nil},
		{"png signature then text", append([]byte("\x89PNG\r\n\x1a\n"), "not really a png"...)},
		{"png cut short", pngData[:len(pngData)/2]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Process(tc.data); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("Process err = %v, want ErrUnsupported", err)
			}
			if _, err := Scaled(tc.data, 1536); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("Scaled err = %v, want ErrUnsupported", err)
			}
		})
	}
}

func TestImaging_SameBytesSameID(t *testing.T) {
	img := gradient(64, 32)
	data := pngOf(t, img)
	a, b := process(t, data), process(t, bytes.Clone(data))
	sum := sha256.Sum256(data)
	if a.ID != hex.EncodeToString(sum[:]) {
		t.Fatalf("ID = %s, want the hex sha256 of the bytes", a.ID)
	}
	if b.ID != a.ID || !bytes.Equal(b.ReplyJPEG, a.ReplyJPEG) {
		t.Fatalf("same bytes processed twice: IDs %s / %s, reply copies equal %v", a.ID, b.ID, bytes.Equal(b.ReplyJPEG, a.ReplyJPEG))
	}
	changed := gradient(64, 32)
	changed.SetRGBA(0, 0, color.RGBA{1, 2, 3, 255})
	for name, other := range map[string][]byte{
		"one pixel changed":   pngOf(t, changed),
		"same pixels as jpeg": jpegOf(t, img),
	} {
		if p := process(t, other); p.ID == a.ID {
			t.Errorf("%s: same ID %s", name, p.ID)
		}
	}
}

func TestImaging_Scaled(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		w, h int
	}{
		{"large png", pngOf(t, gradient(1600, 800)), 1536, 768},
		{"tall jpeg", jpegOf(t, gradient(600, 1800)), 512, 1536},
		{"small png kept", pngOf(t, gradient(1000, 500)), 1000, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Scaled(tc.data, 1536)
			if err != nil {
				t.Fatalf("Scaled: %v", err)
			}
			if b := decodeJPEG(t, out).Bounds(); b.Dx() != tc.w || b.Dy() != tc.h {
				t.Fatalf("Scaled JPEG is %v, want %d×%d", b, tc.w, tc.h)
			}
		})
	}
}
