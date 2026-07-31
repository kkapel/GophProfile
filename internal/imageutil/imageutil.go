// Package imageutil содержит операции над изображениями.
package imageutil

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg" // регистрация декодера JPEG
	_ "image/png"  // регистрация декодера PNG
	"io"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp" // регистрация декодера WebP
)

// jpegQuality — качество миниатюр в формате JPEG.
const jpegQuality = 85

// Decode читает изображение из потока и определяет его размеры.
func Decode(r io.Reader) (image.Image, error) {
	// image.Decode выбирает декодер по сигнатуре файла среди
	// зарегистрированных пустыми импортами выше.
	img, _, err := image.Decode(r)
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	return img, nil
}

// Thumbnail создаёт миниатюру заданного размера в формате JPEG.
// Изображение обрезается по центру, чтобы заполнить весь квадрат
// без искажения пропорций.
func Thumbnail(img image.Image, width, height int) ([]byte, error) {
	resized := imaging.Fill(img, width, height, imaging.Center, imaging.Lanczos)

	var buf bytes.Buffer
	if err := imaging.Encode(&buf, resized, imaging.JPEG, imaging.JPEGQuality(jpegQuality)); err != nil {
		return nil, fmt.Errorf("encode thumbnail: %w", err)
	}

	return buf.Bytes(), nil
}

// Dimensions возвращает ширину и высоту изображения.
func Dimensions(img image.Image) (width, height int32) {
	bounds := img.Bounds()

	//nolint:gosec // размеры изображения заведомо помещаются в int32
	return int32(bounds.Dx()), int32(bounds.Dy())
}
