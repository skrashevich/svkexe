package integrations

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// DecodeInput bounds credential input and rejects unknown JSON fields and trailing data.
func DecodeInput(r io.Reader) (Input, error) {
	var in Input
	body, err := io.ReadAll(io.LimitReader(r, 65537))
	if err != nil || len(body) > 65536 {
		return in, ErrInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, ErrInvalid
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return in, ErrInvalid
	}
	return in, nil
}

// HTTPHandler serves the shared owner-scoped REST contract. Authentication is supplied by the caller.
func (s *Service) HTTPHandler(owner func(*http.Request) string, provider func(*http.Request) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		id := owner(r)
		if id == "" {
			http.Error(w, "unauthorized", 401)
			return
		}
		var result any
		var err error
		switch r.Method {
		case http.MethodGet:
			result, err = s.List(r.Context(), id)
		case http.MethodPut:
			var in Input
			in, err = DecodeInput(http.MaxBytesReader(w, r.Body, 65536))
			if err == nil {
				err = s.Save(r.Context(), id, provider(r), in)
			}
		case http.MethodDelete:
			err = s.Delete(r.Context(), id, provider(r))
		default:
			http.Error(w, "method not allowed", 405)
			return
		}
		if err != nil {
			status, msg := PublicError(err)
			http.Error(w, msg, status)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(204)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

// PublicError returns fixed, secret-free messages for all management adapters.
func PublicError(err error) (int, string) {
	switch {
	case errors.Is(err, ErrInvalid):
		return http.StatusBadRequest, "invalid integration configuration"
	case errors.Is(err, ErrRejected):
		return http.StatusBadRequest, "credentials rejected by the external service; check that the token is valid and has not expired or been revoked"
	case errors.Is(err, ErrVerificationUnavailable):
		return http.StatusServiceUnavailable, "could not verify credentials with the external service; try again later"
	default:
		return http.StatusInternalServerError, "integration operation failed"
	}
}
