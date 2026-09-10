package runtime

import (
	"errors"
	"testing"
)

func TestIsAlreadyRunningError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "exact lower-case match",
			err:  errors.New("already running"),
			want: true,
		},
		{
			name: "mixed-case message",
			err:  errors.New("Instance is Already Running"),
			want: true,
		},
		{
			name: "different error",
			err:  errors.New("container not found"),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAlreadyRunningError(tc.err); got != tc.want {
				t.Fatalf("isAlreadyRunningError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
