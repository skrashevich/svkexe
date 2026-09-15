package metadata

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// IMDSv2 session tokens.
//
// A token is the VM it was issued to plus an expiry, authenticated with a key
// that lives only in this process. Nothing is stored: the token carries its own
// claim and the signature is what makes it unforgeable.
//
// The alternative — a map of issued tokens — is what this deliberately is not.
// One VM looping `PUT /latest/api/token` with the maximum six-hour lifetime
// would grow that map without bound, because nothing expires for six hours, and
// every other VM's token check would queue behind the sweep. There is no
// revocation to lose by going stateless, since a stored token could not be
// revoked either.
//
// The key is regenerated on every start, so a restart invalidates outstanding
// tokens. That is correct rather than unfortunate: a caller whose token stops
// working asks for another, which is the flow IMDSv2 clients already implement.
type tokenStore struct {
	now func() time.Time
	key []byte
}

// tokenKeyBytes is the HMAC key length; 32 bytes matches SHA-256's block
// security and is what crypto/hmac recommends.
const tokenKeyBytes = 32

// nonceBytes is what keeps two tokens issued in the same second distinct.
const nonceBytes = 8

func newTokenStore(now func() time.Time) *tokenStore {
	if now == nil {
		now = time.Now
	}
	key := make([]byte, tokenKeyBytes)
	if _, err := rand.Read(key); err != nil {
		// crypto/rand does not fail on any platform this runs on, and a nil key
		// would make every token verify against every other. Leaving the key
		// empty is not an option, so this is fatal to token issuing rather than
		// silently weakening it: issue() reports the failure and the service
		// answers 503 for token requests while still serving IMDSv1.
		return &tokenStore{now: now}
	}
	return &tokenStore{now: now, key: key}
}

// issue mints a token for one container, valid for ttl.
func (s *tokenStore) issue(container string, ttl time.Duration) (string, error) {
	if len(s.key) == 0 {
		return "", fmt.Errorf("metadata: no token key available")
	}
	// A nonce, so two requests in the same second are two tokens rather than one
	// string issued twice. Nothing depends on it — the signature is what makes a
	// token unforgeable — but a session token that repeats is a surprise, and one
	// leaked token should not be every token of that second.
	nonce := make([]byte, nonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("metadata: generate token: %w", err)
	}
	expiry := s.now().Add(ttl).Unix()
	payload := strconv.FormatInt(expiry, 10) +
		"." + base64.RawURLEncoding.EncodeToString([]byte(container)) +
		"." + base64.RawURLEncoding.EncodeToString(nonce)
	return payload + "." + s.sign(payload), nil
}

// valid reports whether token is live and was issued to container.
func (s *tokenStore) valid(token, container string) bool {
	if len(s.key) == 0 {
		return false
	}
	payload, signature, ok := cutLast(token, ".")
	if !ok {
		return false
	}
	// The signature is checked before anything in the payload is believed, so a
	// forged expiry or container never gets read as one.
	if subtle.ConstantTimeCompare([]byte(signature), []byte(s.sign(payload))) != 1 {
		return false
	}
	rawExpiry, rest, ok := strings.Cut(payload, ".")
	if !ok {
		return false
	}
	rawContainer, _, ok := strings.Cut(rest, ".")
	if !ok {
		return false
	}
	expiry, err := strconv.ParseInt(rawExpiry, 10, 64)
	if err != nil || !s.now().Before(time.Unix(expiry, 0)) {
		return false
	}
	claimed, err := base64.RawURLEncoding.DecodeString(rawContainer)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(claimed, []byte(container)) == 1
}

func (s *tokenStore) sign(payload string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// cutLast splits around the last occurrence of sep, which is what separates a
// signature from a payload that contains separators of its own.
func cutLast(s, sep string) (before, after string, found bool) {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return s, "", false
}
