package displays

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"

	qrcode "github.com/yeqown/go-qrcode/v2"
)

const (
	qrQuietZoneModules = 4
	qrModulePixels     = 8
)

// EnrollmentQRCodeDataURL encodes one Administrator claim URL as a PNG QR image.
func EnrollmentQRCodeDataURL(claimURL string) (string, error) {
	code, err := qrcode.New(claimURL)
	if err != nil {
		return "", errors.New("encode Display Enrollment QR code")
	}
	writer := &pngQRWriter{}
	if err := code.Save(writer); err != nil {
		return "", errors.New("render Display Enrollment QR code")
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(writer.buffer.Bytes()), nil
}

type pngQRWriter struct {
	buffer bytes.Buffer
}

func (writer *pngQRWriter) Write(matrix qrcode.Matrix) error {
	bitmap := matrix.Bitmap()
	width := (matrix.Width() + 2*qrQuietZoneModules) * qrModulePixels
	canvas := image.NewGray(image.Rect(0, 0, width, width))
	for index := range canvas.Pix {
		canvas.Pix[index] = 0xff
	}
	for y, row := range bitmap {
		for x, active := range row {
			if !active {
				continue
			}
			startX := (x + qrQuietZoneModules) * qrModulePixels
			startY := (y + qrQuietZoneModules) * qrModulePixels
			for pixelY := startY; pixelY < startY+qrModulePixels; pixelY++ {
				for pixelX := startX; pixelX < startX+qrModulePixels; pixelX++ {
					canvas.SetGray(pixelX, pixelY, color.Gray{Y: 0})
				}
			}
		}
	}
	if err := png.Encode(&writer.buffer, canvas); err != nil {
		return errors.New("encode Display Enrollment QR PNG")
	}
	return nil
}

func (*pngQRWriter) Close() error { return nil }
