package imageutil

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

func createTestPNG(t *testing.T, width, height int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	// Fill with a solid color using direct pixel buffer access (much faster than per-pixel Set).
	pix := img.Pix
	for i := 0; i < len(pix); i += 4 {
		pix[i] = 100
		pix[i+1] = 150
		pix[i+2] = 200
		pix[i+3] = 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("Failed to create test image: %v", err)
	}
	return buf.Bytes()
}

func TestImageDimensionsWithinPatchLimit(t *testing.T) {
	width, height, resized := imageDimensionsWithinPatchLimit(4832, 6496, 32, 30000)
	if !resized {
		t.Fatal("expected oversized image to be resized")
	}
	if got := imagePatchCount(width, height, 32); got > 30000 {
		t.Fatalf("resized dimensions %dx%d use %d patches, want <= 30000", width, height, got)
	}
	if width < 4700 || height < 6300 {
		t.Fatalf("resized dimensions %dx%d discard more detail than necessary", width, height)
	}

	width, height, resized = imageDimensionsWithinPatchLimit(4800, 6400, 32, 30000)
	if resized || width != 4800 || height != 6400 {
		t.Fatalf("within-limit image changed to %dx%d (resized=%v)", width, height, resized)
	}
}

func TestResizeImageToPatchLimit(t *testing.T) {
	data := createTestPNG(t, 100, 80)
	resized, format, width, height, didResize, err := ResizeImageToPatchLimit(data, 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !didResize || format != "png" {
		t.Fatalf("didResize=%v format=%q, want true/png", didResize, format)
	}
	if got := imagePatchCount(width, height, 10); got > 20 {
		t.Fatalf("resized dimensions %dx%d use %d patches, want <= 20", width, height, got)
	}
	if len(resized) == 0 {
		t.Fatal("resized image is empty")
	}
}

func TestResizeImageToPatchLimitGIF(t *testing.T) {
	frame := image.NewPaletted(image.Rect(10, 10, 60, 50), []color.Color{color.Black, color.White})
	animation := gif.GIF{
		Image: []*image.Paletted{frame},
		Delay: []int{0},
		Config: image.Config{
			ColorModel: frame.Palette,
			Width:      100,
			Height:     80,
		},
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &animation); err != nil {
		t.Fatal(err)
	}
	resized, format, width, height, didResize, err := ResizeImageToPatchLimit(buf.Bytes(), 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !didResize || format != "png" {
		t.Fatalf("didResize=%v format=%q, want true/png", didResize, format)
	}
	if width != 50 || height != 40 {
		t.Fatalf("resized logical canvas = %dx%d, want 50x40", width, height)
	}
	if got := imagePatchCount(width, height, 10); got > 20 {
		t.Fatalf("resized dimensions %dx%d use %d patches, want <= 20", width, height, got)
	}
	decoded, err := png.Decode(bytes.NewReader(resized))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, alpha := decoded.At(0, 0).RGBA(); alpha != 0 {
		t.Fatalf("GIF canvas background alpha = %d, want transparent", alpha)
	}
}

func TestResizeImage(t *testing.T) {
	tests := []struct {
		name       string
		width      int
		height     int
		maxDim     int
		wantResize bool
		wantMaxDim int
	}{
		{"small image", 80, 60, 200, false, 80},
		{"at limit", 200, 200, 200, false, 200},
		{"width exceeds", 300, 100, 200, true, 200},
		{"height exceeds", 100, 300, 200, true, 200},
		{"both exceed", 300, 300, 200, true, 200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := createTestPNG(t, tt.width, tt.height)
			resized, format, didResize, err := ResizeImage(data, tt.maxDim)
			if err != nil {
				t.Fatalf("ResizeImage() error = %v", err)
			}
			if didResize != tt.wantResize {
				t.Errorf("ResizeImage() didResize = %v, want %v", didResize, tt.wantResize)
			}
			if format != "png" {
				t.Errorf("ResizeImage() format = %v, want png", format)
			}
			if didResize {
				// Verify the resized image dimensions
				config, _, err := image.DecodeConfig(bytes.NewReader(resized))
				if err != nil {
					t.Fatalf("Failed to decode resized image: %v", err)
				}
				if config.Width > tt.maxDim || config.Height > tt.maxDim {
					t.Errorf("Resized image %dx%d still exceeds max %d", config.Width, config.Height, tt.maxDim)
				}
			} else {
				if !bytes.Equal(resized, data) {
					t.Error("Expected original data when no resize needed")
				}
			}
		})
	}
}

func TestResizeImageJPEG(t *testing.T) {
	// Create a test JPEG image
	img := image.NewNRGBA(image.Rect(0, 0, 300, 100))
	pix := img.Pix
	for i := 0; i < len(pix); i += 4 {
		pix[i] = 100
		pix[i+1] = 150
		pix[i+2] = 200
		pix[i+3] = 255
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("Failed to create test JPEG image: %v", err)
	}
	data := buf.Bytes()

	resized, format, didResize, err := ResizeImage(data, 200)
	if err != nil {
		t.Fatalf("ResizeImage() error = %v", err)
	}
	if !didResize {
		t.Error("Expected resize for large JPEG image")
	}
	if format != "jpeg" {
		t.Errorf("ResizeImage() format = %v, want jpeg", format)
	}

	// Verify the resized image dimensions
	config, _, err := image.DecodeConfig(bytes.NewReader(resized))
	if err != nil {
		t.Fatalf("Failed to decode resized image: %v", err)
	}
	if config.Width > 200 || config.Height > 200 {
		t.Errorf("Resized image %dx%d still exceeds max 200", config.Width, config.Height)
	}
}

func TestResizeImageErrors(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		maxDim  int
		wantErr bool
	}{
		{
			name:    "empty data",
			data:    []byte{},
			maxDim:  2000,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := ResizeImage(tt.data, tt.maxDim)
			if (err != nil) != tt.wantErr {
				t.Errorf("ResizeImage() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestResizeImageNoResizeNeeded(t *testing.T) {
	data := createTestPNG(t, 80, 60)
	resized, format, didResize, err := ResizeImage(data, 200)
	if err != nil {
		t.Fatalf("ResizeImage() error = %v", err)
	}
	if didResize {
		t.Error("Expected no resize for small image")
	}
	if format != "png" {
		t.Errorf("ResizeImage() format = %v, want png", format)
	}
	if !bytes.Equal(resized, data) {
		t.Error("Expected original data when no resize needed")
	}
}

func TestDecodeDimensions(t *testing.T) {
	png := createTestPNG(t, 137, 91)
	w, h, err := DecodeDimensions(png)
	if err != nil {
		t.Fatalf("DecodeDimensions(png) error: %v", err)
	}
	if w != 137 || h != 91 {
		t.Errorf("DecodeDimensions(png) = (%d, %d), want (137, 91)", w, h)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 64, 48)), nil); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	w, h, err = DecodeDimensions(buf.Bytes())
	if err != nil {
		t.Fatalf("DecodeDimensions(jpeg) error: %v", err)
	}
	if w != 64 || h != 48 {
		t.Errorf("DecodeDimensions(jpeg) = (%d, %d), want (64, 48)", w, h)
	}

	if _, _, err := DecodeDimensions([]byte("not an image")); err == nil {
		t.Errorf("DecodeDimensions(garbage) succeeded; want error")
	}
}

func TestValidate(t *testing.T) {
	// A complete PNG validates.
	if err := Validate(createTestPNG(t, 32, 24)); err != nil {
		t.Errorf("Validate(good png) = %v, want nil", err)
	}

	// A complete JPEG validates.
	var jbuf bytes.Buffer
	if err := jpeg.Encode(&jbuf, image.NewNRGBA(image.Rect(0, 0, 20, 20)), nil); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	if err := Validate(jbuf.Bytes()); err != nil {
		t.Errorf("Validate(good jpeg) = %v, want nil", err)
	}

	// A truncated PNG — valid header, cut-short pixel data (the flaky-upload
	// case) — must be rejected. This is the regression: such bytes sniff as a
	// PNG and even expose dimensions, but decoding them (or sending them to a
	// model) fails.
	full := createTestPNG(t, 64, 64)
	truncated := full[:len(full)/2]
	if _, _, derr := DecodeDimensions(truncated); derr != nil {
		t.Fatalf("precondition: truncated PNG should still expose dimensions, got %v", derr)
	}
	if err := Validate(truncated); err == nil {
		t.Errorf("Validate(truncated png) = nil, want error")
	}

	// Non-image garbage surfaces as image.ErrFormat (no decoder claims it), the
	// same signal as an unsupported-but-valid format. Validate deliberately
	// lets that through — the read_image caller already rejects non-images via
	// http.DetectContentType before ever calling Validate, whose sole job is to
	// catch a *recognized* format with corrupt/truncated payload.
	if err := Validate([]byte("definitely not an image")); err != nil {
		t.Errorf("Validate(garbage) = %v, want nil (ErrFormat is not our concern)", err)
	}

	// WebP is a supported Responses API image format, so its decoder is
	// registered and malformed payloads must be rejected rather than passed to
	// the provider.
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 16)...)
	if err := Validate(webp); err == nil {
		t.Error("Validate(malformed webp) = nil, want error")
	}
}
