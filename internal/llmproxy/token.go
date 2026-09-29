package llmproxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
)

type vmIdentity struct {
	Container string `json:"container"`
	Owner     string `json:"owner"`
}

// VMToken is scoped to an existing VM and its owner; ownership is checked on every request.
func VMToken(key []byte, container, owner string) string {
	payload, _ := json.Marshal(vmIdentity{container, owner})
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("svkexe-llm-vm-v1:" + encoded))
	return "vm1." + encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func ParseVMToken(key []byte, token string) (container, owner string, ok bool) {
	if len(key) != 32 {
		return "", "", false
	}
	rest, ok := strings.CutPrefix(token, "vm1.")
	if !ok {
		return "", "", false
	}
	payload, signature, ok := strings.Cut(rest, ".")
	if !ok {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", "", false
	}
	var id vmIdentity
	if json.Unmarshal(raw, &id) != nil || id.Container == "" || id.Owner == "" {
		return "", "", false
	}
	expected := VMToken(key, id.Container, id.Owner)
	_, expectedSignature, _ := strings.Cut(strings.TrimPrefix(expected, "vm1."), ".")
	return id.Container, id.Owner, hmac.Equal([]byte(signature), []byte(expectedSignature))
}
