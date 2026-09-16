package server

import (
	"context"
	"testing"

	"shelley.exe.dev/db/generated"
)

func TestSlugifyModelID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		endpoint  string
		modelName string
		want      string
	}{
		{
			name:      "openai default https port omitted",
			endpoint:  "https://api.openai.com/v1",
			modelName: "gpt-4o",
			want:      "gpt-4o-api-openai-com",
		},
		{
			name:      "localhost with non-default port",
			endpoint:  "http://localhost:11434",
			modelName: "llama3.1",
			want:      "llama3-1-localhost-11434",
		},
		{
			name:      "http default port 80 omitted",
			endpoint:  "http://localhost:80/v1",
			modelName: "foo",
			want:      "foo-localhost",
		},
		{
			name:      "namespaced model name uses last segment",
			endpoint:  "https://api.fireworks.ai/inference/v1",
			modelName: "accounts/fireworks/models/glm-5p2",
			want:      "glm-5p2-api-fireworks-ai",
		},
		{
			name:      "https on explicit non-default port kept",
			endpoint:  "https://example.com:8443/v1",
			modelName: "model",
			want:      "model-example-com-8443",
		},
		{
			name:      "messy characters collapse",
			endpoint:  "https://My_Host.Example.COM",
			modelName: "Some Model!!Name",
			want:      "some-model-name-my-host-example-com",
		},
		{
			name:      "unparseable endpoint treated as host",
			endpoint:  "not a url",
			modelName: "m",
			want:      "m-not-a-url",
		},
		{
			name:      "empty everything falls back to custom",
			endpoint:  "",
			modelName: "",
			want:      "custom",
		},
		{
			name:      "trailing slash model name ignored",
			endpoint:  "https://api.openai.com/v1",
			modelName: "models/",
			want:      "models-api-openai-com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := slugifyModelID(tt.endpoint, tt.modelName); got != tt.want {
				t.Errorf("slugifyModelID(%q, %q) = %q, want %q", tt.endpoint, tt.modelName, got, tt.want)
			}
		})
	}
}

func TestGenerateUniqueModelID(t *testing.T) {
	t.Parallel()
	srv, _, _ := newTestServer(t)
	ctx := context.Background()

	endpoint := "https://api.openai.com/v1"
	modelName := "gpt-4o"

	id1, err := srv.generateUniqueModelID(ctx, endpoint, modelName)
	if err != nil {
		t.Fatal(err)
	}
	if want := "gpt-4o-api-openai-com"; id1 != want {
		t.Fatalf("got %q, want %q", id1, want)
	}

	// Persist a model with that id, then expect a -2 suffix.
	if _, err := srv.db.CreateModel(ctx, generated.CreateModelParams{
		ModelID:      id1,
		DisplayName:  "GPT-4o",
		ProviderType: "openai",
		Endpoint:     endpoint,
		ApiKey:       "sk-test",
		ModelName:    modelName,
		MaxTokens:    200000,
		ImageSupport: "auto",
	}); err != nil {
		t.Fatal(err)
	}
	id2, err := srv.generateUniqueModelID(ctx, endpoint, modelName)
	if err != nil {
		t.Fatal(err)
	}
	if want := "gpt-4o-api-openai-com-2"; id2 != want {
		t.Fatalf("got %q, want %q", id2, want)
	}
}
