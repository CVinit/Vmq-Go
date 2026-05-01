package app

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/liyue201/goqr"
	"github.com/skip2/go-qrcode"
)

const (
	maxQRCodePayloadLength = 2048
	maxQRCodePayloadForm   = maxQRCodePayloadLength*3 + 1024
	maxQRCodeImageBytes    = 5 << 20
	maxQRCodeBase64Length  = 7 << 20
	maxQRCodeBase64Form    = maxQRCodeBase64Length*3 + 1024
	maxQRCodePixels        = 16_000_000
)

func encodeQRCode(content string) ([]byte, error) {
	return qrcode.Encode(content, qrcode.Medium, 200)
}

func decodeQRCodeFromBase64(raw string) (string, error) {
	if len(raw) > maxQRCodeBase64Length {
		return "", errors.New("qr code payload too large")
	}
	payload, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", err
	}
	return decodeQRCodeBytes(payload)
}

func decodeQRCodeBytes(payload []byte) (string, error) {
	if len(payload) > maxQRCodeImageBytes {
		return "", errors.New("qr code image too large")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxQRCodePixels {
		return "", errors.New("qr code image dimensions too large")
	}
	img, _, err := image.Decode(bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	symbols, err := goqr.Recognize(img)
	if err != nil {
		return "", err
	}
	if len(symbols) == 0 {
		return "", errors.New("qr code not found")
	}
	return string(symbols[0].Payload), nil
}
