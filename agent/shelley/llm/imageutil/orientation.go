package imageutil

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
)

// Orientation is an EXIF orientation tag value (1..8). It says how a decoder
// must rotate and flip the stored pixels to display the image as intended.
type Orientation int

// SwapsDimensions reports whether displaying the image transposes its stored
// width and height, which the four 90-degree orientations do.
func (o Orientation) SwapsDimensions() bool {
	return o >= 5 && o <= 8
}

// DisplayDimensions maps stored dimensions to the ones a viewer shows.
func (o Orientation) DisplayDimensions(width, height int) (int, int) {
	if o.SwapsDimensions() {
		return height, width
	}
	return width, height
}

// OrientationNormal is the identity: stored pixels are already display pixels.
// Also what we report for images with no EXIF orientation at all, which is the
// overwhelming majority (every PNG, and any JPEG that has been re-encoded).
const OrientationNormal Orientation = 1

// DecodeOrientation returns the EXIF orientation of a JPEG.
//
// Browsers honor this tag, and Go's image decoders ignore it, so the two
// disagree about an image's dimensions -- and about where any given pixel is --
// whenever it is not 1. Anything reported to the UI in "the source file's
// coordinates" has to be in display coordinates to match what the user sees and
// what an image tool like ImageMagick (which also honors the tag) will crop.
//
// Only the tag is read, not the rest of EXIF, and a missing or unparseable tag
// is OrientationNormal rather than an error: the common case is no EXIF at all,
// and an image whose metadata we cannot read is still an image we can show.
func DecodeOrientation(data []byte) Orientation {
	exif := exifSegment(data)
	if exif == nil {
		return OrientationNormal
	}
	if o, ok := exifOrientation(exif); ok {
		return o
	}
	return OrientationNormal
}

// exifSegment returns the TIFF header and body of a JPEG's APP1/Exif segment.
//
// A JPEG is a sequence of marker segments: 0xFF, a marker byte, then (for the
// markers that have a payload) a two-byte big-endian length that includes those
// two bytes. Walking them is enough to find APP1 without decoding any pixels.
func exifSegment(data []byte) []byte {
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 { // SOI
		return nil
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return nil // not at a marker: malformed, or we lost the thread
		}
		marker := data[i+1]
		// Standalone markers: no length, no payload.
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		// Start of image data; EXIF, if any, came before it.
		if marker == 0xDA || marker == 0xD9 {
			return nil
		}
		length := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if length < 2 || i+2+length > len(data) {
			return nil
		}
		payload := data[i+4 : i+2+length]
		if marker == 0xE1 { // APP1
			const prefix = "Exif\x00\x00"
			if bytes.HasPrefix(payload, []byte(prefix)) {
				return payload[len(prefix):]
			}
		}
		i += 2 + length
	}
	return nil
}

// exifOrientation reads tag 0x0112 from the first IFD of a TIFF block.
func exifOrientation(tiff []byte) (Orientation, bool) {
	if len(tiff) < 8 {
		return 0, false
	}
	var order binary.ByteOrder
	switch {
	case bytes.HasPrefix(tiff, []byte("II")):
		order = binary.LittleEndian
	case bytes.HasPrefix(tiff, []byte("MM")):
		order = binary.BigEndian
	default:
		return 0, false
	}
	if order.Uint16(tiff[2:4]) != 42 { // TIFF magic
		return 0, false
	}
	ifd := int(order.Uint32(tiff[4:8]))
	if ifd < 8 || ifd+2 > len(tiff) {
		return 0, false
	}
	count := int(order.Uint16(tiff[ifd : ifd+2]))
	// Each entry is 12 bytes: tag, type, component count, then the value --
	// inline when it fits in four bytes, as it does for a single SHORT.
	const entrySize = 12
	for e := 0; e < count; e++ {
		at := ifd + 2 + e*entrySize
		if at+entrySize > len(tiff) {
			return 0, false
		}
		if order.Uint16(tiff[at:at+2]) != 0x0112 { // Orientation
			continue
		}
		v := Orientation(order.Uint16(tiff[at+8 : at+10]))
		if v < 1 || v > 8 {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

// DecodeDisplayDimensions returns the dimensions a viewer that honors EXIF
// orientation will show for the image in data, which is what the UI measures
// regions against.
func DecodeDisplayDimensions(data []byte) (width, height int, err error) {
	w, h, err := DecodeDimensions(data)
	if err != nil {
		return 0, 0, fmt.Errorf("decode display dimensions: %w", err)
	}
	w, h = DecodeOrientation(data).DisplayDimensions(w, h)
	return w, h, nil
}

// orientedImage presents stored pixels in display orientation without first
// allocating a full-size transformed copy. The resize scaler reads through
// this view and writes the final, smaller image directly.
type orientedImage struct {
	image.Image
	orientation Orientation
	bounds      image.Rectangle
}

func newOrientedImage(src image.Image, orientation Orientation) image.Image {
	if orientation == OrientationNormal {
		return src
	}
	width, height := orientation.DisplayDimensions(src.Bounds().Dx(), src.Bounds().Dy())
	return orientedImage{
		Image:       src,
		orientation: orientation,
		bounds:      image.Rect(0, 0, width, height),
	}
}

func (img orientedImage) Bounds() image.Rectangle { return img.bounds }
func (img orientedImage) ColorModel() color.Model { return img.Image.ColorModel() }

func (img orientedImage) At(x, y int) color.Color {
	bounds := img.Image.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	var sourceX, sourceY int
	switch img.orientation {
	case 2:
		sourceX, sourceY = width-1-x, y
	case 3:
		sourceX, sourceY = width-1-x, height-1-y
	case 4:
		sourceX, sourceY = x, height-1-y
	case 5:
		sourceX, sourceY = y, x
	case 6:
		sourceX, sourceY = y, height-1-x
	case 7:
		sourceX, sourceY = width-1-y, height-1-x
	case 8:
		sourceX, sourceY = width-1-y, x
	default:
		sourceX, sourceY = x, y
	}
	return img.Image.At(bounds.Min.X+sourceX, bounds.Min.Y+sourceY)
}
