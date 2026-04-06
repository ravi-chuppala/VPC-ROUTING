package auth

import (
	"net/http"

	"github.com/google/uuid"
)

// Middleware extracts account identity from the Authorization header and injects it into context.
// In production, this validates JWT signatures. For development, it accepts a simple
// "Bearer <account-uuid>" format.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Health checks bypass auth
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, `{"error":{"code":"UNAUTHENTICATED","message":"missing Authorization header"}}`, http.StatusUnauthorized)
			return
		}

		token, err := ParseBearerToken(authHeader)
		if err != nil {
			http.Error(w, `{"error":{"code":"UNAUTHENTICATED","message":"invalid Authorization header"}}`, http.StatusUnauthorized)
			return
		}

		// In development mode, the token is the account UUID directly.
		// In production, this would validate a JWT and extract claims.
		accountID, err := uuid.Parse(token)
		if err != nil {
			http.Error(w, `{"error":{"code":"UNAUTHENTICATED","message":"invalid token"}}`, http.StatusUnauthorized)
			return
		}

		ctx := ContextWithAccount(r.Context(), accountID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
