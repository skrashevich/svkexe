package api

import (
	"time"

	dbpkg "github.com/skrashevich/svkexe/internal/db"
)

// userResponse is the public JSON shape for user records (no password hash).
type userResponse struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
}

func userToResponse(u *dbpkg.User) userResponse {
	return userResponse{
		ID:          u.ID,
		Email:       u.Email,
		DisplayName: u.DisplayName,
		Role:        u.Role,
		CreatedAt:   u.CreatedAt,
	}
}

func usersToResponse(users []*dbpkg.User) []userResponse {
	out := make([]userResponse, len(users))
	for i, u := range users {
		out[i] = userToResponse(u)
	}
	return out
}
