// Package imaging checks uploaded persona and chat images and makes the smaller
// JPEG copies sent to models and shown in the panel, using only the standard
// library. A JPEG's EXIF orientation is applied: images are used upright, as
// they are shown.
package imaging

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
)

// Upload limits, and the long edge and quality of the reply copy.
const (
	MaxBytes    = 8 << 20
	MaxEdge     = 8000
	MaxPixels   = 25_000_000
	ReplyEdge   = 768
	JPEGQuality = 85
)

// ViewEdge is the long edge of a chat image's view copy, shown when opened and
// described by the vision model.
const ViewEdge = 1536

// tileEdge is the edge in pixels of the image tiles Tokens counts.
const tileEdge = 28

var ErrUnsupported, ErrTooLarge = errors.New("imaging: unsupported image"), errors.New("imaging: image too large")

// Processed is a checked upload. ID is the lowercase hex sha256 of Original,
// the bytes as uploaded; MIME is image/png or image/jpeg, and Width and
// Height are the original's. ReplyJPEG is the reply copy, at most ReplyEdge
// pixels on its long edge, and ReplyTokens its Tokens estimate.
type Processed struct {
	ID, MIME                             string
	Width, Height                        int
	Original, ReplyJPEG                  []byte
	ReplyWidth, ReplyHeight, ReplyTokens int
}

// Process checks data as an uploaded image and makes its reply copy. The
// format is sniffed from the bytes, never taken from a declared type; only
// PNG and JPEG are accepted, and data that does not decode is ErrUnsupported
// too. The dimensions are checked before any pixel is decoded: data over
// MaxBytes, an edge over MaxEdge or more than MaxPixels pixels is
// ErrTooLarge. Transparent pixels are flattened onto white, and a JPEG is
// turned upright by its EXIF orientation (Width and Height are then the
// upright size).
func Process(data []byte) (Processed, error) {
	img, mime, err := decode(data)
	if err != nil {
		return Processed{}, err
	}
	reply := shrink(img, ReplyEdge)
	replyJPEG, err := encodeJPEG(reply)
	if err != nil {
		return Processed{}, err
	}
	sum := sha256.Sum256(data)
	w, h := reply.Bounds().Dx(), reply.Bounds().Dy()
	return Processed{
		ID:          hex.EncodeToString(sum[:]),
		MIME:        mime,
		Width:       img.Bounds().Dx(),
		Height:      img.Bounds().Dy(),
		Original:    data,
		ReplyJPEG:   replyJPEG,
		ReplyWidth:  w,
		ReplyHeight: h,
		ReplyTokens: Tokens(w, h),
	}, nil
}

// Chat is a checked chat image: a view copy (ViewEdge) and a reply copy
// (ReplyEdge), both JPEG. The original is not kept.
type Chat struct {
	ViewJPEG                             []byte
	ViewWidth, ViewHeight                int
	ReplyJPEG                            []byte
	ReplyWidth, ReplyHeight, ReplyTokens int
}

// ProcessChat checks data as Process does (sniffed PNG/JPEG, limits,
// flattened onto white, EXIF orientation applied), decodes it once and
// shrinks both copies from the upright pixels.
func ProcessChat(data []byte) (Chat, error) {
	img, _, err := decode(data)
	if err != nil {
		return Chat{}, err
	}
	view, reply := shrink(img, ViewEdge), shrink(img, ReplyEdge)
	viewJPEG, err := encodeJPEG(view)
	if err != nil {
		return Chat{}, err
	}
	replyJPEG, err := encodeJPEG(reply)
	if err != nil {
		return Chat{}, err
	}
	rw, rh := reply.Bounds().Dx(), reply.Bounds().Dy()
	return Chat{
		ViewJPEG:    viewJPEG,
		ViewWidth:   view.Bounds().Dx(),
		ViewHeight:  view.Bounds().Dy(),
		ReplyJPEG:   replyJPEG,
		ReplyWidth:  rw,
		ReplyHeight: rh,
		ReplyTokens: Tokens(rw, rh),
	}, nil
}

// Scaled checks and flattens data as Process does and returns a JPEG copy of
// it at most maxEdge pixels on its long edge; it never enlarges, and a
// maxEdge of 0 or less keeps the size.
func Scaled(data []byte, maxEdge int) ([]byte, error) {
	img, _, err := decode(data)
	if err != nil {
		return nil, err
	}
	return encodeJPEG(shrink(img, maxEdge))
}

// Tokens estimates the model tokens of a w×h image: one per started 28×28
// tile, plus 2.
func Tokens(w, h int) int {
	return ceilDiv(w, tileEdge)*ceilDiv(h, tileEdge) + 2
}

func ceilDiv(a, b int) int {
	return (a + b - 1) / b
}

// decode checks data (see Process) and returns its pixels drawn over white,
// a JPEG's turned upright by its EXIF orientation, and its sniffed MIME type.
func decode(data []byte) (*image.RGBA, string, error) {
	if len(data) > MaxBytes {
		return nil, "", fmt.Errorf("%w: %d bytes", ErrTooLarge, len(data))
	}
	var decodeConfig func(io.Reader) (image.Config, error)
	var decodeImage func(io.Reader) (image.Image, error)
	mime := http.DetectContentType(data)
	switch mime {
	case "image/png":
		decodeConfig, decodeImage = png.DecodeConfig, png.Decode
	case "image/jpeg":
		decodeConfig, decodeImage = jpeg.DecodeConfig, jpeg.Decode
	default:
		return nil, "", fmt.Errorf("%w: %s", ErrUnsupported, mime)
	}
	cfg, err := decodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, "", fmt.Errorf("%w: %d×%d", ErrUnsupported, cfg.Width, cfg.Height)
	}
	if cfg.Width > MaxEdge || cfg.Height > MaxEdge || cfg.Width*cfg.Height > MaxPixels {
		return nil, "", fmt.Errorf("%w: %d×%d", ErrTooLarge, cfg.Width, cfg.Height)
	}
	src, err := decodeImage(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	img := flatten(src)
	if mime == "image/jpeg" {
		img = orient(img, exifOrientation(data))
	}
	return img, mime, nil
}

// flatten draws src over a white background.
func flatten(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Over)
	return dst
}

func encodeJPEG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: JPEGQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fit returns the size of a w×h image scaled down to at most maxEdge on its
// long edge, keeping the aspect ratio; it never enlarges, and a maxEdge of 0
// or less keeps the size.
func fit(w, h, maxEdge int) (int, int) {
	if maxEdge <= 0 || max(w, h) <= maxEdge {
		return w, h
	}
	if w >= h {
		return maxEdge, max(1, (h*maxEdge+w/2)/w)
	}
	return max(1, (w*maxEdge+h/2)/h), maxEdge
}

// share is a source pixel and the part of a destination pixel it covers.
type share struct {
	src    int
	weight float64
}

// shares lists, for each of dst pixels along a line of src pixels, the
// source pixels its area covers and their shares, which add up to 1.
func shares(src, dst int) [][]share {
	scale := float64(src) / float64(dst)
	out := make([][]share, dst)
	for d := range out {
		lo, hi := float64(d)*scale, float64(d+1)*scale
		for s := int(lo); s < src && float64(s) < hi; s++ {
			covered := min(hi, float64(s+1)) - max(lo, float64(s))
			out[d] = append(out[d], share{s, covered / scale})
		}
	}
	return out
}

// shrink scales img down to fit maxEdge (see fit) by averaging the source
// area each destination pixel covers, or returns img when it already fits.
// img must be opaque and start at the origin.
func shrink(img *image.RGBA, maxEdge int) *image.RGBA {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	dw, dh := fit(w, h, maxEdge)
	if dw == w && dh == h {
		return img
	}
	cols, rows := shares(w, dw), shares(h, dh)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	row := make([]float64, 3*dw)
	sum := make([]float64, 3*dw)
	for dy, rowShares := range rows {
		clear(sum)
		for _, rs := range rowShares {
			shrinkRow(img, rs.src, cols, row)
			for i, v := range row {
				sum[i] += v * rs.weight
			}
		}
		out := dst.Pix[dy*dst.Stride:]
		for dx := range dw {
			for c := range 3 {
				out[4*dx+c] = uint8(min(255, sum[3*dx+c]+0.5))
			}
			out[4*dx+3] = 255
		}
	}
	return dst
}

// shrinkRow writes source row y of img, averaged over cols, to out as red,
// green and blue per destination pixel.
func shrinkRow(img *image.RGBA, y int, cols [][]share, out []float64) {
	pix := img.Pix[y*img.Stride:]
	for dx, colShares := range cols {
		var r, g, b float64
		for _, cs := range colShares {
			p := pix[4*cs.src : 4*cs.src+3]
			r += float64(p[0]) * cs.weight
			g += float64(p[1]) * cs.weight
			b += float64(p[2]) * cs.weight
		}
		out[3*dx], out[3*dx+1], out[3*dx+2] = r, g, b
	}
}
