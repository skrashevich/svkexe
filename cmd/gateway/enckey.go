package main

import (
	"encoding/hex"
	"fmt"
)

// deriveEncKey decodes GATEWAY_ENC_KEY as hex into a 32-byte AES-256 key.
func deriveEncKey(hexStr string) ([]byte, error) {
	if hexStr == "" {
		return nil, fmt.Errorf("GATEWAY_ENC_KEY is required (generate with: openssl rand -hex 32)")
	}
	key, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("GATEWAY_ENC_KEY must be valid hex: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("GATEWAY_ENC_KEY must be 32 bytes (64 hex characters), got %d bytes", len(key))
	}
	return key, nil
}
