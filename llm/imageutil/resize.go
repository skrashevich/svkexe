// Package imageutil provides image manipulation utilities.
package imageutil

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// DecodeDimensions returns the pixel width and height of the image encoded in
// data. It reads only the image header (no full decode), so it is cheap.
func DecodeDimensions(data []byte) (width, height int, err error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, fmt.Errorf("decode image config: %w", err)
	}
	return cfg.Width, cfg.Height, nil
}

// Validate fully decodes data to confirm it is a complete, well-formed image.
// Unlike DecodeDimensions (header-only) and http.DetectContentType (magic-byte
// sniff), this walks every pixel chunk, so it catches truncated or otherwise
// corrupt files whose header still looks valid. A partial upload over a flaky
// link is the motivating case: its PNG header advertises the right dimensions
// but the pixel data is cut short. Embedding such bytes in a message makes the
// provider reject the whole request (400 "could not process image"), which
// wedges the conversation permanently. Validating here turns that into a
// recoverable tool error instead.
//
// Formats with registered decoders (PNG, JPEG, GIF, and WebP) surface decode
// errors. Other formats return image.ErrFormat; we cannot verify those here,
// so we let them through rather than reject a valid image we cannot decode.
func Validate(data []byte) error {
	if _, _, err := decodeImageForResize(data); err != nil {
		if errors.Is(err, image.ErrFormat) {
			return nil
		}
		return fmt.Errorf("decode image: %w", err)
	}
	return nil
}

// ResizeImageToPatchLimit resizes an image only when its grid of square patches
// exceeds maxPatches. Providers commonly account for vision inputs in patches,
// so a byte or longest-edge limit alone cannot prevent oversized requests.
func ResizeImageToPatchLimit(data []byte, patchSize, maxPatches int) (resized []byte, format string, width, height int, didResize bool, err error) {
	cfg, detectedFormat, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		if errors.Is(err, image.ErrFormat) {
			return data, "", 0, 0, false, nil
		}
		return nil, "", 0, 0, false, fmt.Errorf("decode image config: %w", err)
	}

	width, height, didResize = imageDimensionsWithinPatchLimit(cfg.Width, cfg.Height, patchSize, maxPatches)
	if !didResize {
		return data, detectedFormat, width, height, false, nil
	}

	resized, format, didResize, err = ResizeImage(data, max(width, height))
	if err != nil {
		return nil, "", 0, 0, false, err
	}
	width, height, err = DecodeDimensions(resized)
	if err != nil {
		return nil, "", 0, 0, false, err
	}
	if patches := imagePatchCount(width, height, patchSize); patches > maxPatches {
		return nil, "", 0, 0, false, fmt.Errorf("resized image still requires %d patches (limit %d)", patches, maxPatches)
	}
	return resized, format, width, height, didResize, nil
}

func imageDimensionsWithinPatchLimit(width, height, patchSize, maxPatches int) (int, int, bool) {
	if width <= 0 || height <= 0 || patchSize <= 0 || maxPatches <= 0 || imagePatchCount(width, height, patchSize) <= maxPatches {
		return width, height, false
	}

	longest := max(width, height)
	bestWidth, bestHeight := 1, 1
	for low, high := 1, longest-1; low <= high; {
		candidateLongest := low + (high-low)/2
		candidateWidth, candidateHeight := scaledImageDimensions(width, height, candidateLongest)
		if imagePatchCount(candidateWidth, candidateHeight, patchSize) <= maxPatches {
			bestWidth, bestHeight = candidateWidth, candidateHeight
			low = candidateLongest + 1
		} else {
			high = candidateLongest - 1
		}
	}
	return bestWidth, bestHeight, true
}

func scaledImageDimensions(width, height, maxDimension int) (int, int) {
	if width > height {
		return maxDimension, max(1, height*maxDimension/width)
	}
	return max(1, width*maxDimension/height), maxDimension
}

func imagePatchCount(width, height, patchSize int) int {
	if width <= 0 || height <= 0 || patchSize <= 0 {
		return 0
	}
	return ((width + patchSize - 1) / patchSize) * ((height + patchSize - 1) / patchSize)
}

func decodeImageForResize(data []byte) (image.Image, string, error) {
	img, detectedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	if detectedFormat != "gif" {
		return img, detectedFormat, nil
	}

	decoded, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	if len(decoded.Image) == 0 {
		return nil, "", fmt.Errorf("GIF contains no frames")
	}
	canvas := image.NewRGBA(image.Rect(0, 0, decoded.Config.Width, decoded.Config.Height))
	frame := decoded.Image[0]
	draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)
	return canvas, detectedFormat, nil
}

// ResizeImage resizes an image if any dimension exceeds maxDimension.
// Returns the resized image bytes and the format ("png" or "jpeg").
// If no resize is needed, returns the original data unchanged.
func ResizeImage(data []byte, maxDimension int) (resized []byte, format string, didResize bool, err error) {
	img, detectedFormat, err := decodeImageForResize(data)
	if err != nil {
		return nil, "", false, fmt.Errorf("failed to decode image: %w", err)
	}
	img = newOrientedImage(img, DecodeOrientation(data))

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	if width <= maxDimension && height <= maxDimension {
		return data, detectedFormat, false, nil
	}

	// Calculate new dimensions preserving aspect ratio
	newWidth, newHeight := scaledImageDimensions(width, height, maxDimension)

	// Create resized image
	resizedImg := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
	xdraw.BiLinear.Scale(resizedImg, resizedImg.Bounds(), img, bounds, xdraw.Over, nil)

	// Encode to the same format
	var buf bytes.Buffer
	switch strings.ToLower(detectedFormat) {
	case "jpeg", "jpg":
		err = jpeg.Encode(&buf, resizedImg, &jpeg.Options{Quality: 85})
		format = "jpeg"
	default:
		err = png.Encode(&buf, resizedImg)
		format = "png"
	}

	if err != nil {
		return nil, "", false, fmt.Errorf("failed to encode resized image: %w", err)
	}

	return buf.Bytes(), format, true, nil
}
