package media

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
)

func imageDimensions(data []byte) (int, int, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, fmt.Errorf("decode image dimensions: %w", err)
	}
	return config.Width, config.Height, nil
}

func compressImage(data []byte, maxBytes int64, maxLength int) ([]byte, error) {
	if maxBytes <= 1 || maxLength <= 1 {
		return nil, fmt.Errorf("image compression limits are too small")
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	maxDimension := maxLength - 1
	if width > maxDimension || height > maxDimension {
		scale := float64(maxDimension) / float64(max(width, height))
		width = max(1, int(float64(width)*scale))
		height = max(1, int(float64(height)*scale))
	}

	for {
		candidate := resizeImage(source, width, height)
		for quality := 92; quality >= 35; quality -= 5 {
			encoded, err := encodeJPEG(candidate, quality)
			if err != nil {
				return nil, err
			}
			if int64(len(encoded)) < maxBytes {
				return encoded, nil
			}
		}
		if width == 1 && height == 1 {
			break
		}
		width = max(1, int(float64(width)*0.85))
		height = max(1, int(float64(height)*0.85))
		if width > maxDimension || height > maxDimension {
			scale := float64(maxDimension) / float64(max(width, height))
			width = max(1, int(float64(width)*scale))
			height = max(1, int(float64(height)*scale))
		}
	}
	return nil, fmt.Errorf("compressed image still exceeds %d bytes", maxBytes)
}

func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	var output bytes.Buffer
	if err := jpeg.Encode(&output, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("encode JPEG: %w", err)
	}
	return output.Bytes(), nil
}

func resizeImage(source image.Image, width, height int) image.Image {
	bounds := source.Bounds()
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	for y := 0; y < height; y++ {
		fy := (float64(y)+0.5)*float64(bounds.Dy())/float64(height) - 0.5
		for x := 0; x < width; x++ {
			fx := (float64(x)+0.5)*float64(bounds.Dx())/float64(width) - 0.5
			pixel := bilinearNRGBA(source, bounds, fx, fy)
			alpha := uint32(pixel.A)
			canvas.Set(x, y, color.NRGBA{
				R: uint8((uint32(pixel.R)*alpha + 255*(255-alpha)) / 255),
				G: uint8((uint32(pixel.G)*alpha + 255*(255-alpha)) / 255),
				B: uint8((uint32(pixel.B)*alpha + 255*(255-alpha)) / 255),
				A: 255,
			})
		}
	}
	return canvas
}

func bilinearNRGBA(source image.Image, bounds image.Rectangle, fx, fy float64) color.NRGBA {
	x0 := clamp(int(fx), 0, bounds.Dx()-1)
	y0 := clamp(int(fy), 0, bounds.Dy()-1)
	x1 := min(x0+1, bounds.Dx()-1)
	y1 := min(y0+1, bounds.Dy()-1)
	dx, dy := fx-float64(int(fx)), fy-float64(int(fy))
	if fx < 0 {
		dx = 0
	}
	if fy < 0 {
		dy = 0
	}
	pixels := [4]color.NRGBA{
		color.NRGBAModel.Convert(source.At(bounds.Min.X+x0, bounds.Min.Y+y0)).(color.NRGBA),
		color.NRGBAModel.Convert(source.At(bounds.Min.X+x1, bounds.Min.Y+y0)).(color.NRGBA),
		color.NRGBAModel.Convert(source.At(bounds.Min.X+x0, bounds.Min.Y+y1)).(color.NRGBA),
		color.NRGBAModel.Convert(source.At(bounds.Min.X+x1, bounds.Min.Y+y1)).(color.NRGBA),
	}
	blend := func(values [4]uint8) uint8 {
		top := float64(values[0])*(1-dx) + float64(values[1])*dx
		bottom := float64(values[2])*(1-dx) + float64(values[3])*dx
		return uint8(top*(1-dy) + bottom*dy + 0.5)
	}
	return color.NRGBA{
		R: blend([4]uint8{pixels[0].R, pixels[1].R, pixels[2].R, pixels[3].R}),
		G: blend([4]uint8{pixels[0].G, pixels[1].G, pixels[2].G, pixels[3].G}),
		B: blend([4]uint8{pixels[0].B, pixels[1].B, pixels[2].B, pixels[3].B}),
		A: blend([4]uint8{pixels[0].A, pixels[1].A, pixels[2].A, pixels[3].A}),
	}
}

func clamp(value, low, high int) int {
	return min(max(value, low), high)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
