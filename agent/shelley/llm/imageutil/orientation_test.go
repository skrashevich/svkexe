package imageutil

import (
	"bytes"
	"encoding/base64"
	"image"
	"testing"
)

// A 100x50 JPEG tagged EXIF Orientation=6 (rotate 90 CW to display), so a
// viewer that honors the tag shows it 50x100.
const rotatedJPEG100x50 = "/9j/4AAQSkZJRgABAQAAAQABAAD/4QAiRXhpZgAATU0AKgAAAAgAAQESAAMAAAABAAYAAAAAAAD/2wBDABsSFBcUERsXFhceHBsgKEIrKCUlKFE6PTBCYFVlZF9VXVtqeJmBanGQc1tdhbWGkJ6jq62rZ4C8ybqmx5moq6T/2wBDARweHigjKE4rK06kbl1upKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKSkpKT/wAARCAAyAGQDASIAAhEBAxEB/8QAGQAAAwEBAQAAAAAAAAAAAAAAAAIDAQQG/8QAFxABAQEBAAAAAAAAAAAAAAAAAAECEv/EABoBAQEAAwEBAAAAAAAAAAAAAAMBAAIEBgX/xAAYEQEBAQEBAAAAAAAAAAAAAAAAARECEv/aAAwDAQACEQMRAD8A87MGmFZg0w11pO0Zg0wtMNmF0s7SmGzC0waYZpJ2jMGmFZg0wuknaMwaYWmGzC6SdpTBphWYNMLpJ2jwF+AzW/txTBphaYbMObXnZ2lMNmFpg0wzSztGYNMKzBphdJO0phswtMNmF0k7SmDTCswaYXSTtGYNMLTDZhmknaPAdHAXW/txTBphWYNMOXXnZ2jMGmFZg0wulnaUw2YWmDTC6SdozBphWYNMLpJ2jMGmFphswzSTtKYbMLTBphdJO0eAvwF1v7cENAHO+FGw0AUkNGwBSQ0NAGEjYaAKWGhoApI0AK3f/9k="

func decodeFixture(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(rotatedJPEG100x50)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return data
}

func TestDecodeOrientationRotatedJPEG(t *testing.T) {
	data := decodeFixture(t)
	// Premise: Go's decoders ignore the tag, so the stored size is landscape.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if cfg.Width != 100 || cfg.Height != 50 {
		t.Fatalf("stored dimensions = %dx%d, want 100x50", cfg.Width, cfg.Height)
	}

	if got := DecodeOrientation(data); got != 6 {
		t.Errorf("DecodeOrientation = %d, want 6", got)
	}
	w, h, err := DecodeDisplayDimensions(data)
	if err != nil {
		t.Fatalf("DecodeDisplayDimensions: %v", err)
	}
	if w != 50 || h != 100 {
		t.Errorf("DecodeDisplayDimensions = %dx%d, want 50x100", w, h)
	}
}

func TestResizeImageAutoOrientsJPEG(t *testing.T) {
	resized, format, didResize, err := ResizeImage(decodeFixture(t), 60)
	if err != nil {
		t.Fatal(err)
	}
	if !didResize || format != "jpeg" {
		t.Fatalf("didResize=%v format=%q, want true/jpeg", didResize, format)
	}
	width, height, err := DecodeDimensions(resized)
	if err != nil {
		t.Fatal(err)
	}
	if width != 30 || height != 60 {
		t.Fatalf("resized display dimensions = %dx%d, want 30x60", width, height)
	}
	if orientation := DecodeOrientation(resized); orientation != OrientationNormal {
		t.Fatalf("resized orientation = %d, want normal", orientation)
	}
}

func TestDecodeOrientationAbsentIsNormal(t *testing.T) {
	png := createTestPNG(t, 137, 91)
	if got := DecodeOrientation(png); got != OrientationNormal {
		t.Errorf("DecodeOrientation(png) = %d, want %d", got, OrientationNormal)
	}
	w, h, err := DecodeDisplayDimensions(png)
	if err != nil {
		t.Fatalf("DecodeDisplayDimensions: %v", err)
	}
	if w != 137 || h != 91 {
		t.Errorf("DecodeDisplayDimensions(png) = %dx%d, want 137x91", w, h)
	}
}

func TestDecodeOrientationGarbageIsNormal(t *testing.T) {
	for name, data := range map[string][]byte{
		"empty":          {},
		"not a jpeg":     []byte("hello there, not an image at all"),
		"truncated soi":  {0xFF},
		"soi then junk":  {0xFF, 0xD8, 0x00, 0x00, 0x00},
		"app1 too short": {0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x02},
	} {
		if got := DecodeOrientation(data); got != OrientationNormal {
			t.Errorf("DecodeOrientation(%s) = %d, want %d", name, got, OrientationNormal)
		}
	}
}

func TestOrientationSwapsDimensions(t *testing.T) {
	for o := Orientation(1); o <= 8; o++ {
		want := o >= 5
		if got := o.SwapsDimensions(); got != want {
			t.Errorf("Orientation(%d).SwapsDimensions() = %v, want %v", o, got, want)
		}
		w, h := o.DisplayDimensions(100, 50)
		if want {
			if w != 50 || h != 100 {
				t.Errorf("Orientation(%d).DisplayDimensions(100, 50) = %dx%d, want 50x100", o, w, h)
			}
		} else if w != 100 || h != 50 {
			t.Errorf("Orientation(%d).DisplayDimensions(100, 50) = %dx%d, want 100x50", o, w, h)
		}
	}
}
