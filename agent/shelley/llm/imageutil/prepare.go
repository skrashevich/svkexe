package imageutil

import (
	"fmt"
	"net/http"
	"strings"
)

// Prepared contains image bytes ready to send to an LLM.
type Prepared struct {
	Data      []byte
	MediaType string
	Width     int
	Height    int
	// SourceWidth and SourceHeight are the dimensions of the input image,
	// before any downscaling, as a viewer that honors EXIF orientation displays
	// them. They differ from Width/Height when Resized, and are what callers
	// must report when they also hand out the source path: coordinates into the
	// LLM-facing copy do not address the original file.
	//
	// Display dimensions rather than stored ones because that is the space the
	// UI and image tools both work in -- a JPEG tagged "rotate 90" is 100x50 in
	// the file and 50x100 everywhere a person or ImageMagick looks at it.
	//
	// Zero only for formats this binary cannot decode, which are also the ones
	// it cannot resize — so they are always set when Resized is true.
	SourceWidth  int
	SourceHeight int
	// SourceOrientation is the EXIF orientation of the input image, i.e. the
	// transform a viewer applies that a raw pixel read does not. Callers that
	// hand out coordinates into the source file must report it when it is not
	// OrientationNormal: tools crop stored pixels, so they need to be told to
	// auto-orient first to agree with what the user saw.
	SourceOrientation Orientation
	Converted         bool
	Resized           bool
}

// Prepare validates image data and fits it within a model's advertised limits.
// HEIC is converted because Go's image package does not decode it directly.
//
// Recognized formats are fully decoded before being returned. Header sniffing
// alone can accept a truncated upload; embedding those bytes can make the
// provider reject the entire request and permanently wedge the conversation.
//
// Dimension overflow is fixed transparently by downscaling because callers do
// not request a specific image size. Byte overflow that remains after resizing
// is returned as an error so the caller can recompress or choose another image
// instead of sending a request the provider will reject. source is included in
// errors so the caller knows which input needs attention.
func Prepare(data []byte, source string, maxDimension, maxBytes int) (Prepared, error) {
	converted := false
	if IsHEIC(data) {
		var err error
		data, err = ConvertHEICToPNG(data)
		if err != nil {
			return Prepared{}, fmt.Errorf("convert HEIC image %s: %w", source, err)
		}
		converted = true
	}

	mediaType := http.DetectContentType(data)
	if !strings.HasPrefix(mediaType, "image/") {
		return Prepared{}, fmt.Errorf("file is not an image: %s", mediaType)
	}
	if err := Validate(data); err != nil {
		return Prepared{}, fmt.Errorf("image file appears corrupt or truncated (%s); re-upload or pick a different file: %w", source, err)
	}

	resized := false
	format := strings.TrimPrefix(mediaType, "image/")
	// Dimensions before any downscaling. A format with no decoder registered in
	// this binary (such as SVG or AVIF) fails here, leaving these zero — but such
	// an image also cannot be resized below, so Width/Height stay equal to the
	// source's and callers never need the pre-resize numbers. Whenever Resized
	// is true, these are populated.
	sourceWidth, sourceHeight, _ := DecodeDisplayDimensions(data)
	sourceOrientation := DecodeOrientation(data)
	if maxDimension > 0 {
		// ResizeImage returns the original bytes when the image already fits.
		// If it cannot decode a format such as SVG or AVIF, leave the bytes
		// unchanged and continue to the byte-limit check.
		resizedData, resizedFormat, didResize, err := ResizeImage(data, maxDimension)
		if err == nil {
			data = resizedData
			format = resizedFormat
			resized = didResize
		}
	}
	if maxBytes > 0 && len(data) > maxBytes {
		return Prepared{}, fmt.Errorf(
			"image too large for model: %s is %d bytes (after any auto-resize), model limit is %d bytes; recompress the image (e.g. lower JPEG quality) and try again",
			source, len(data), maxBytes,
		)
	}

	// Also display dimensions: when nothing was resized these bytes are the
	// original's, EXIF tag and all, and the <img> that renders them will rotate.
	// A resize re-encodes without the tag, making the two the same thing.
	width, height, _ := DecodeDisplayDimensions(data)
	return Prepared{
		Data:              data,
		MediaType:         "image/" + format,
		Width:             width,
		Height:            height,
		SourceWidth:       sourceWidth,
		SourceHeight:      sourceHeight,
		SourceOrientation: sourceOrientation,
		Converted:         converted,
		Resized:           resized,
	}, nil
}
