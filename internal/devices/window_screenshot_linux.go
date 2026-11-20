package devices

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"math/bits"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// PlayShutter plays nothing: the shutter is macOS's.
func PlayShutter() {}

func (c *WindowCapture) captureFrontmostMacWindow(context.Context, bool) WindowShot {
	return WindowShot{Status: WindowShotUnsupported, Message: "Window capture through CoreGraphics is only on macOS."}
}

// captureX11Pixels reads the window's own pixels from the X server, as a PNG. Under a compositing
// window manager the window keeps its pixels while covered; without one, a covered part shows
// whatever covers it.
func captureX11Pixels(id int64, width, height int) ([]byte, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	window := xproto.Window(id)
	attributes, err := xproto.GetWindowAttributes(conn, window).Reply()
	if err != nil {
		return nil, err
	}
	geometry, err := xproto.GetGeometry(conn, xproto.Drawable(window)).Reply()
	if err != nil {
		return nil, err
	}
	width, height = min(width, int(geometry.Width)), min(height, int(geometry.Height))
	if width <= 0 || height <= 0 {
		return nil, nil
	}
	reply, err := xproto.GetImage(conn, xproto.ImageFormatZPixmap, xproto.Drawable(window), 0, 0, uint16(width), uint16(height), 0xffffffff).Reply()
	if err != nil {
		return nil, err
	}
	setup := xproto.Setup(conn)
	var visual *xproto.VisualInfo
	for _, screen := range setup.Roots {
		for _, depth := range screen.AllowedDepths {
			for index := range depth.Visuals {
				if depth.Visuals[index].VisualId == attributes.Visual {
					visual = &depth.Visuals[index]
				}
			}
		}
	}
	bitsPerPixel := 0
	scanlinePad := 32
	for _, format := range setup.PixmapFormats {
		if format.Depth == reply.Depth {
			bitsPerPixel, scanlinePad = int(format.BitsPerPixel), int(format.ScanlinePad)
		}
	}
	if visual == nil || (bitsPerPixel != 32 && bitsPerPixel != 24) {
		return nil, fmt.Errorf("the window's pixel format (depth %d, %d bits per pixel) can't be read", reply.Depth, bitsPerPixel)
	}
	stride := ((width*bitsPerPixel + scanlinePad - 1) / scanlinePad) * scanlinePad / 8
	if len(reply.Data) < stride*height {
		return nil, nil
	}
	var order binary.ByteOrder = binary.LittleEndian
	if setup.ImageByteOrder == xproto.ImageOrderMSBFirst {
		order = binary.BigEndian
	}
	channel := func(pixel, mask uint32) uint8 {
		if mask == 0 {
			return 0
		}
		value := (pixel & mask) >> bits.TrailingZeros32(mask)
		span := bits.OnesCount32(mask)
		if span >= 8 {
			return uint8(value >> (span - 8))
		}
		return uint8(value * 255 / (1<<span - 1))
	}
	picture := image.NewNRGBA(image.Rect(0, 0, width, height))
	bytesPerPixel := bitsPerPixel / 8
	for y := range height {
		row := reply.Data[y*stride:]
		for x := range width {
			at := row[x*bytesPerPixel:]
			var pixel uint32
			if bytesPerPixel == 4 {
				pixel = order.Uint32(at)
			} else if order == binary.LittleEndian {
				pixel = uint32(at[0]) | uint32(at[1])<<8 | uint32(at[2])<<16
			} else {
				pixel = uint32(at[0])<<16 | uint32(at[1])<<8 | uint32(at[2])
			}
			out := picture.Pix[y*picture.Stride+x*4:]
			out[0], out[1], out[2], out[3] = channel(pixel, visual.RedMask), channel(pixel, visual.GreenMask), channel(pixel, visual.BlueMask), 255
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}
