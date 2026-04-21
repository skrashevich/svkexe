// Package ctxkeys defines shared context keys used across api and dashboard packages.
package ctxkeys

// Key is the type for context keys in this package.
type Key string

const (
	// User is the context key for the authenticated *db.User.
	User Key = "user"
	// UserID is the context key for the authenticated user ID string.
	UserID Key = "userID"
	// UserEmail is the context key for the authenticated user email string.
	UserEmail Key = "userEmail"
)
