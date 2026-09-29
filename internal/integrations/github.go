package integrations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// GitHub is a PAT connection to github.com. Client is injectable for tests.
// The endpoint is fixed so user input cannot redirect credential verification.
type GitHub struct{ Client *http.Client }

func (GitHub) Descriptor() Descriptor {
	return Descriptor{ID: "github", Authentication: "personal-access-token", Name: "GitHub", Secrets: []Field{{Name: "token", Label: "Personal access token", Required: true}}, Credentials: []string{"token"}}
}
func (g GitHub) Validate(ctx context.Context, in Input) error {
	token := in.Secrets["token"]
	if token == "" || len(token) > 4096 || strings.ContainsAny(token, " \t\r\n") {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return ErrVerificationUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "svkexe-integration-verification")
	client := http.Client{}
	if g.Client != nil {
		client = *g.Client
	}
	// Never follow a redirect with a credential, including to another GitHub host.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return ErrVerificationUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrRejected
	}
	if resp.StatusCode != http.StatusOK {
		return ErrVerificationUnavailable
	}
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&user); err != nil || user.ID <= 0 || user.Login == "" {
		return ErrVerificationUnavailable
	}
	return nil
}
func (GitHub) Credential(in Input, name string) (string, error) { return in.Secrets[name], nil }
