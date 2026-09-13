package media

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestCompressImageProducesBoundedJPEGWithWhiteBackground(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 80, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 80; x++ {
			if x < 40 {
				source.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
			}
		}
	}
	var input bytes.Buffer
	if err := png.Encode(&input, source); err != nil {
		t.Fatal(err)
	}

	output, err := compressImage(input.Bytes(), 16*1024, 32)
	if err != nil {
		t.Fatal(err)
	}
	if len(output) >= 16*1024 {
		t.Fatalf("compressed size = %d", len(output))
	}
	decoded, err := jpeg.Decode(bytes.NewReader(output))
	if err != nil {
		t.Fatalf("decode JPEG: %v", err)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() >= 32 || bounds.Dy() >= 32 || bounds.Dx() != 31 || bounds.Dy() != 15 {
		t.Fatalf("compressed bounds = %v", bounds)
	}
	white := color.NRGBAModel.Convert(decoded.At(bounds.Max.X-2, bounds.Max.Y/2)).(color.NRGBA)
	if white.R < 240 || white.G < 240 || white.B < 240 {
		t.Fatalf("transparent background was not white: %#v", white)
	}
}
