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

func encodeQRCode(content string) ([]byte, error) {
	return qrcode.Encode(content, qrcode.Medium, 200)
}

func decodeQRCodeFromBase64(raw string) (string, error) {
	payload, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", err
	}
	return decodeQRCodeBytes(payload)
}

func decodeQRCodeBytes(payload []byte) (string, error) {
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
