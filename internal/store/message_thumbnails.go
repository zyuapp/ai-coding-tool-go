package store

import (
	"bytes"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"math"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const maxThumbnailSourcePixels = 64_000_000

var errNoPreview = errors.New("Image cannot be previewed.")

// MessageThumbnail cuts a preview of at most 640×360 from an image, or refuses one it cannot
// decode or that is too large to decode.
func MessageThumbnail(data []byte) ([]byte, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width*config.Height > maxThumbnailSourcePixels {
		return nil, errNoPreview
	}
	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errNoPreview
	}
	width, height := float64(config.Width), float64(config.Height)
	scale := math.Min(1, math.Min(640/width, 360/height))
	target := image.NewNRGBA(image.Rect(0, 0, max(1, int(math.Round(width*scale))), max(1, int(math.Round(height*scale)))))
	if scale == 1 {
		draw.Draw(target, target.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
	} else {
		draw.CatmullRom.Scale(target, target.Bounds(), decoded, decoded.Bounds(), draw.Src, nil)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, target); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
