// Package integrations manages owner-scoped connections to external services.
package integrations

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/skrashevich/svkexe/internal/db"
)

var ErrInvalid = errors.New("invalid integration configuration")
var ErrRejected = errors.New("external service rejected credentials")
var ErrVerificationUnavailable = errors.New("credential verification unavailable; try again later")
var safeName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Field describes a string field. Secret fields are always write-only.
type Field struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
}
type Descriptor struct {
	Authentication string   `json:"authentication"`
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Config         []Field  `json:"config"`
	Secrets        []Field  `json:"secrets"`
	Credentials    []string `json:"credentials"`
}
type Input struct {
	Config  map[string]string `json:"config"`
	Secrets map[string]string `json:"secrets"`
}

// Provider implements validation and credential projection for one system.
// Validation errors are deliberately not exposed to clients, as they may contain secrets.
type Provider interface {
	Descriptor() Descriptor
	Validate(context.Context, Input) error
	Credential(Input, string) (string, error)
}
type Connection struct {
	Provider  string            `json:"provider"`
	Config    map[string]string `json:"config"`
	UpdatedAt string            `json:"updated_at"`
}
type Service struct {
	db        *db.DB
	key       []byte
	providers map[string]Provider
}

// New builds an immutable registry. Omit providers to use the production registry.
func New(database *db.DB, key []byte, providers ...Provider) *Service {
	if len(providers) == 0 {
		providers = []Provider{GitHub{}}
	}
	s := &Service{db: database, key: slices.Clone(key), providers: map[string]Provider{}}
	for _, p := range providers {
		d := p.Descriptor()
		if !safeName.MatchString(d.ID) || s.providers[d.ID] != nil {
			panic("invalid or duplicate integration provider")
		}
		names := map[string]bool{}
		for _, f := range append(slices.Clone(d.Config), d.Secrets...) {
			if !safeName.MatchString(f.Name) || names[f.Name] {
				panic("invalid or duplicate integration field")
			}
			names[f.Name] = true
		}
		names = map[string]bool{}
		for _, n := range d.Credentials {
			if !safeName.MatchString(n) || names[n] {
				panic("invalid integration credential")
			}
			names[n] = true
		}
		s.providers[d.ID] = p
	}
	return s
}
func (s *Service) Descriptors() []Descriptor {
	out := []Descriptor{}
	for _, p := range s.providers {
		d := p.Descriptor()
		d.Config = slices.Clone(d.Config)
		d.Secrets = slices.Clone(d.Secrets)
		d.Credentials = slices.Clone(d.Credentials)
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b Descriptor) int { return strings.Compare(a.ID, b.ID) })
	return out
}
func (s *Service) Descriptor(id string) (Descriptor, error) {
	p := s.providers[id]
	if p == nil {
		return Descriptor{}, ErrInvalid
	}
	return p.Descriptor(), nil
}
func validFields(values map[string]string, fields []Field) bool {
	for n, v := range values {
		if len(v) > 16384 {
			return false
		}
		if !slices.ContainsFunc(fields, func(f Field) bool { return f.Name == n }) {
			return false
		}
	}
	for _, f := range fields {
		if f.Required && strings.TrimSpace(values[f.Name]) == "" {
			return false
		}
	}
	return true
}
func binding(owner, provider string) []byte {
	b, _ := json.Marshal([]any{owner, provider, 1})
	return b
}
func (s *Service) aead() (cipher.AEAD, error) {
	b, e := aes.NewCipher(s.key)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(b)
}
func (s *Service) Save(ctx context.Context, owner, provider string, in Input) error {
	p := s.providers[provider]
	if owner == "" || p == nil {
		return ErrInvalid
	}
	d := p.Descriptor()
	if !validFields(in.Config, d.Config) || !validFields(in.Secrets, d.Secrets) {
		return ErrInvalid
	}
	if err := p.Validate(ctx, in); err != nil {
		// Keep only safe classifications; a provider error can contain credentials.
		switch {
		case errors.Is(err, ErrRejected):
			return ErrRejected
		case errors.Is(err, ErrInvalid):
			return ErrInvalid
		default:
			return ErrVerificationUnavailable
		}
	}
	if in.Config == nil {
		in.Config = map[string]string{}
	}
	config, err := json.Marshal(in.Config)
	if err != nil {
		return err
	}
	plain, err := json.Marshal(in.Secrets)
	if err != nil {
		return err
	}
	a, err := s.aead()
	if err != nil {
		return err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	encrypted := a.Seal(nonce, nonce, plain, binding(owner, provider))
	_, err = s.db.ExecContext(ctx, `INSERT INTO user_integrations(owner_id,provider,config,credentials) VALUES(?,?,?,?) ON CONFLICT(owner_id,provider) DO UPDATE SET config=excluded.config,credentials=excluded.credentials,schema_version=1,updated_at=CURRENT_TIMESTAMP`, owner, provider, string(config), encrypted)
	return err
}
func (s *Service) List(ctx context.Context, owner string) ([]Connection, error) {
	if owner == "" {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT provider,config,updated_at FROM user_integrations WHERE owner_id=? ORDER BY provider`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		var c Connection
		var config string
		if err = rows.Scan(&c.Provider, &config, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if s.providers[c.Provider] == nil {
			continue
		}
		if err = json.Unmarshal([]byte(config), &c.Config); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Service) Delete(ctx context.Context, owner, provider string) error {
	if owner == "" || s.providers[provider] == nil {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM user_integrations WHERE owner_id=? AND provider=?`, owner, provider)
	return err
}
func (s *Service) Credential(ctx context.Context, owner, provider, name string) (string, error) {
	p := s.providers[provider]
	if owner == "" || p == nil || !slices.Contains(p.Descriptor().Credentials, name) {
		return "", sql.ErrNoRows
	}
	var config string
	var encrypted []byte
	var version int
	err := s.db.QueryRowContext(ctx, `SELECT config,credentials,schema_version FROM user_integrations WHERE owner_id=? AND provider=?`, owner, provider).Scan(&config, &encrypted, &version)
	if err != nil {
		return "", err
	}
	if version != 1 {
		return "", fmt.Errorf("unsupported integration schema")
	}
	a, err := s.aead()
	if err != nil {
		return "", err
	}
	if len(encrypted) < a.NonceSize() {
		return "", fmt.Errorf("invalid encrypted credentials")
	}
	plain, err := a.Open(nil, encrypted[:a.NonceSize()], encrypted[a.NonceSize():], binding(owner, provider))
	if err != nil {
		return "", err
	}
	var in Input
	if err = json.Unmarshal([]byte(config), &in.Config); err != nil {
		return "", err
	}
	if err = json.Unmarshal(plain, &in.Secrets); err != nil {
		return "", err
	}
	return p.Credential(in, name)
}
