package main

import (
	"encoding/hex"
	"testing"
)

func TestDeriveEncKey_ValidHex(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	hexStr := hex.EncodeToString(raw)

	key, err := deriveEncKey(hexStr)
	if err != nil {
		t.Fatalf("deriveEncKey: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("want 32 bytes, got %d", len(key))
	}
	for i := range key {
		if key[i] != raw[i] {
			t.Fatalf("byte %d: want %d got %d", i, raw[i], key[i])
		}
	}
}

func TestDeriveEncKey_Empty(t *testing.T) {
	if _, err := deriveEncKey(""); err == nil {
		t.Fatal("expected error for empty key")
	}
}

func TestDeriveEncKey_WrongLength(t *testing.T) {
	if _, err := deriveEncKey("abcd"); err == nil {
		t.Fatal("expected error for short key")
	}
}
