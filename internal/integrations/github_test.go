package integrations

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type githubTransport func(*http.Request) (*http.Response, error)

func (f githubTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestGitHubValidatesThroughAuthenticatedAPI(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"classic", 200, `{"id":1,"login":"tester"}`, nil},
		{"invalid-or-expired", 401, `{"message":"private-token"}`, ErrRejected},
		{"rate-limit-or-forbidden", 403, `{"message":"private-token"}`, ErrVerificationUnavailable},
		{"rate-limit", 429, `{}`, ErrVerificationUnavailable},
		{"upstream-failure", 503, `{}`, ErrVerificationUnavailable},
		{"redirect", 302, `{}`, ErrVerificationUnavailable},
		{"malformed-json", 200, `bad private-token`, ErrVerificationUnavailable},
		{"missing-identity", 200, `{}`, ErrVerificationUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: githubTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != "https://api.github.com/user" || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer private-token" {
					t.Fatal("wrong authentication request")
				}
				if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) > 10*time.Second {
					t.Fatal("verification has no bounded deadline")
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Location": []string{"https://untrusted.test/"}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})}
			g := GitHub{Client: client}
			err := g.Validate(t.Context(), Input{Secrets: map[string]string{"token": "private-token"}})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if calls != 1 {
				t.Fatal("redirect followed")
			}
			if err != nil && strings.Contains(err.Error(), "private-token") {
				t.Fatal("secret leaked")
			}
		})
	}
}
func TestGitHubNetworkAndCancellationFailClosed(t *testing.T) {
	client := &http.Client{Transport: githubTransport(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("connection failed with private-token")
	})}
	if err := (GitHub{Client: client}).Validate(t.Context(), Input{Secrets: map[string]string{"token": "private-token"}}); !errors.Is(err, ErrVerificationUnavailable) || strings.Contains(err.Error(), "private-token") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	client.Transport = githubTransport(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })
	if err := (GitHub{Client: client}).Validate(ctx, Input{Secrets: map[string]string{"token": "private-token"}}); !errors.Is(err, ErrVerificationUnavailable) {
		t.Fatal(err)
	}
}
func TestGitHubInvalidFormatDoesNotMakeRequest(t *testing.T) {
	client := &http.Client{Transport: githubTransport(func(*http.Request) (*http.Response, error) { t.Fatal("format error sent upstream"); return nil, nil })}
	for _, token := range []string{"", "bad\nvalue", strings.Repeat("a", 4097)} {
		if err := (GitHub{Client: client}).Validate(t.Context(), Input{Secrets: map[string]string{"token": token}}); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
}
func TestGitHubSaveKeepsExistingTokenOnRejection(t *testing.T) {
	s := setup(t)
	reject := false
	s.providers["github"] = GitHub{Client: &http.Client{Transport: githubTransport(func(r *http.Request) (*http.Response, error) {
		status := 200
		if reject {
			status = 401
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":1,"login":"tester"}`)), Request: r}, nil
	})}}
	input := Input{Secrets: map[string]string{"token": "original"}}
	if err := s.Save(t.Context(), "a", "github", input); err != nil {
		t.Fatal(err)
	}
	reject = true
	input.Secrets["token"] = "replacement"
	if err := s.Save(t.Context(), "a", "github", input); !errors.Is(err, ErrRejected) {
		t.Fatal(err)
	}
	if value, err := s.Credential(t.Context(), "a", "github", "token"); err != nil || value != "original" {
		t.Fatal("rejection damaged stored connection")
	}
}
