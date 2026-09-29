package integrations

import "strings"

// GitHub is a static PAT connection to github.com.
type GitHub struct{}

func (GitHub) Descriptor() Descriptor {
	return Descriptor{ID: "github", Authentication: "personal-access-token", Name: "GitHub", Secrets: []Field{{Name: "token", Label: "Personal access token", Required: true}}, Credentials: []string{"token"}}
}
func (GitHub) Validate(in Input) error {
	token := in.Secrets["token"]
	if len(token) > 4096 || strings.ContainsAny(token, " \t\r\n") {
		return ErrInvalid
	}
	return nil
}
func (GitHub) Credential(in Input, name string) (string, error) { return in.Secrets[name], nil }
