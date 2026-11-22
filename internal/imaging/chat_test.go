package imaging

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"testing"
)

func processChat(t *testing.T, data []byte) Chat {
	t.Helper()
	c, err := ProcessChat(data)
	if err != nil {
		t.Fatalf("ProcessChat: %v", err)
	}
	return c
}

func TestProcessChat_Copies(t *testing.T) {
	src := gradient(3000, 1500)
	for y := range 32 {
		for x := range 32 {
			src.SetRGBA(x, y, color.RGBA{})
		}
	}
	c := processChat(t, pngOf(t, src))
	if c.ViewWidth != 1536 || c.ViewHeight != 768 {
		t.Fatalf("view = %d×%d, want 1536×768", c.ViewWidth, c.ViewHeight)
	}
	if c.ReplyWidth != 768 || c.ReplyHeight != 384 {
		t.Fatalf("reply = %d×%d, want 768×384", c.ReplyWidth, c.ReplyHeight)
	}
	if c.ReplyTokens != Tokens(768, 384) {
		t.Fatalf("ReplyTokens = %d, want %d", c.ReplyTokens, Tokens(768, 384))
	}
	view := decodeJPEG(t, c.ViewJPEG)
	if b := view.Bounds(); b.Dx() != 1536 || b.Dy() != 768 {
		t.Fatalf("view JPEG is %v, want 1536×768", b)
	}
	if b := decodeJPEG(t, c.ReplyJPEG).Bounds(); b.Dx() != 768 || b.Dy() != 384 {
		t.Fatalf("reply JPEG is %v, want 768×384", b)
	}
	if r, g, b, _ := view.At(0, 0).RGBA(); r>>8 < 250 || g>>8 < 250 || b>>8 < 250 {
		t.Fatalf("view top-left pixel = %v, want white", view.At(0, 0))
	}
}

func TestProcessChat_NeverEnlarges(t *testing.T) {
	c := processChat(t, jpegOf(t, gradient(100, 50)))
	if c.ViewWidth != 100 || c.ViewHeight != 50 || c.ReplyWidth != 100 || c.ReplyHeight != 50 {
		t.Fatalf("view %d×%d, reply %d×%d; want both 100×50", c.ViewWidth, c.ViewHeight, c.ReplyWidth, c.ReplyHeight)
	}
	for name, data := range map[string][]byte{"view": c.ViewJPEG, "reply": c.ReplyJPEG} {
		if b := decodeJPEG(t, data).Bounds(); b.Dx() != 100 || b.Dy() != 50 {
			t.Errorf("%s JPEG is %v, want 100×50", name, b)
		}
	}
}

func TestProcessChat_Rejects(t *testing.T) {
	var gifData bytes.Buffer
	if err := gif.Encode(&gifData, gradient(16, 16), nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"gif", gifData.Bytes(), ErrUnsupported},
		{"png 9000×10", pngOf(t, image.NewGray(image.Rect(0, 0, 9000, 10))), ErrTooLarge},
		{"over MaxBytes", append(pngOf(t, gradient(16, 16)), make([]byte, MaxBytes)...)[:MaxBytes+1], ErrTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ProcessChat(tc.data); !errors.Is(err, tc.want) {
				t.Fatalf("ProcessChat err = %v, want %v", err, tc.want)
			}
		})
	}
}
