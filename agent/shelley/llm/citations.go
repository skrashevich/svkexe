package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
)

// CitationContext identifies where a cited text block appears in request history.
// Providers decide whether that position supports native citation metadata.
type CitationContext struct {
	Role         MessageRole
	InToolResult bool
}

type citationConversion struct {
	path  string
	types []string
}

// PrepareRequestCitations copies request history and adapts citations before any
// provider filtering or retries. adapt owns citation type and field policy; it
// returns either a text reference or retain=true to preserve the opaque object.
// destination is only a diagnostic label. Logs never include source payloads.
func PrepareRequestCitations(ctx context.Context, req *Request, destination string, adapt func(position CitationContext, kind string, fields map[string]json.RawMessage) (reference string, retain bool, err error)) (*Request, error) {
	if req == nil {
		return nil, fmt.Errorf("prepare citations for %s: nil request", destination)
	}
	out := *req
	out.Messages = slices.Clone(req.Messages)
	var conversions []citationConversion
	for i := range out.Messages {
		content, err := prepareContentCitations(req.Messages[i].Content, CitationContext{Role: req.Messages[i].Role}, adapt, fmt.Sprintf("messages[%d].content", i), &conversions)
		if err != nil {
			return nil, fmt.Errorf("prepare citations for %s: %w", destination, err)
		}
		out.Messages[i].Content = content
	}
	// Report only after the entire request validates; never log source payloads.
	for _, conversion := range conversions {
		slog.InfoContext(ctx, "converted request citations to text references", "destination", destination,
			"path", conversion.path, "count", len(conversion.types), "types", conversion.types)
	}
	return &out, nil
}

func prepareContentCitations(content []Content, position CitationContext, adapt func(CitationContext, string, map[string]json.RawMessage) (string, bool, error), path string, conversions *[]citationConversion) ([]Content, error) {
	out := slices.Clone(content)
	for i := range out {
		c := &out[i]
		citationPath := fmt.Sprintf("%s[%d].citations", path, i)
		raw := bytes.TrimSpace(c.Citations)
		c.Citations = slices.Clone(c.Citations)
		if len(c.Citations) == 0 || bytes.Equal(raw, []byte("null")) {
			// Older persisted history contains literal null rather than nil.
			c.Citations = nil
		} else {
			var entries []json.RawMessage
			if err := json.Unmarshal(raw, &entries); err != nil {
				return nil, fmt.Errorf("%s: expected citation array: %w", citationPath, err)
			}
			if len(entries) > 0 && (c.Type != ContentTypeText || c.MediaType != "") {
				return nil, fmt.Errorf("%s[0]: citations require text content", citationPath)
			}
			var retained [][]byte
			var converted []string
			for k, entry := range entries {
				entryPath := fmt.Sprintf("%s[%d]", citationPath, k)
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(entry, &fields); err != nil || fields == nil {
					return nil, fmt.Errorf("%s: expected citation object", entryPath)
				}
				kind, err := CitationString(fields, "type", true, false)
				if err != nil || kind == "" {
					return nil, fmt.Errorf("%s: type must be a nonempty string", entryPath)
				}
				reference, retain, err := adapt(position, kind, fields)
				if err != nil {
					return nil, fmt.Errorf("%s (%s): %w", entryPath, kind, err)
				}
				if retain {
					retained = append(retained, entry)
					continue
				}
				if reference == "" {
					return nil, fmt.Errorf("%s (%s): citation conversion returned no source reference", entryPath, kind)
				}
				c.Text += reference
				converted = append(converted, kind)
			}
			if len(entries) == 0 || (len(converted) > 0 && len(retained) == 0) {
				c.Citations = nil
			} else if len(converted) > 0 {
				// Keep retained objects byte-for-byte, including opaque fields.
				c.Citations = append([]byte("["), bytes.Join(retained, []byte(","))...)
				c.Citations = append(c.Citations, ']')
			}
			if len(converted) > 0 {
				*conversions = append(*conversions, citationConversion{path: citationPath, types: converted})
			}
		}
		var err error
		c.ToolResult, err = prepareContentCitations(c.ToolResult, CitationContext{Role: position.Role, InToolResult: true}, adapt, fmt.Sprintf("%s[%d].tool_result", path, i), conversions)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CitationString reads a string field using the caller's presence/null policy.
func CitationString(fields map[string]json.RawMessage, name string, required, nullable bool) (string, error) {
	raw, ok := fields[name]
	if !ok && !required {
		return "", nil
	}
	if nullable && bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	var value string
	if !ok || bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return value, nil
}

// CitationNonnegativeInteger validates an optional integer field.
func CitationNonnegativeInteger(fields map[string]json.RawMessage, name string) error {
	if raw, ok := fields[name]; ok {
		var index int
		if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &index) != nil || index < 0 {
			return fmt.Errorf("%s must be a nonnegative integer", name)
		}
	}
	return nil
}

// FormatCitationURLReference renders validated source fields as plain text.
func FormatCitationURLReference(url, title, quote string) string {
	reference := "\n\nSource: "
	if title != "" {
		reference += title + " — "
	}
	reference += url
	if quote != "" {
		reference += "\nCited text: " + quote
	}
	return reference
}
