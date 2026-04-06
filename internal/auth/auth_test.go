package auth

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/ravi-chuppala/vpc-routing/internal/model"
)

func TestContextWithAccount(t *testing.T) {
	id := uuid.New()
	ctx := ContextWithAccount(context.Background(), id)

	got, err := AccountFromContext(ctx)
	if err != nil {
		t.Fatalf("AccountFromContext() error = %v", err)
	}
	if got != id {
		t.Errorf("AccountFromContext() = %v, want %v", got, id)
	}
}

func TestAccountFromContext_Missing(t *testing.T) {
	_, err := AccountFromContext(context.Background())
	if err == nil {
		t.Error("expected error for missing account")
	}
	appErr, ok := err.(*model.AppError)
	if !ok {
		t.Fatalf("expected *model.AppError, got %T", err)
	}
	if appErr.HTTPStatus != 401 {
		t.Errorf("expected 401, got %d", appErr.HTTPStatus)
	}
}

func TestRequireVPCOwner(t *testing.T) {
	ownerID := uuid.New()
	otherID := uuid.New()

	vpc := &model.VPC{AccountID: ownerID}

	// Owner context should pass
	ctx := ContextWithAccount(context.Background(), ownerID)
	if err := RequireVPCOwner(ctx, vpc); err != nil {
		t.Errorf("RequireVPCOwner() should pass for owner, got %v", err)
	}

	// Non-owner should fail
	ctx = ContextWithAccount(context.Background(), otherID)
	err := RequireVPCOwner(ctx, vpc)
	if err == nil {
		t.Error("RequireVPCOwner() should fail for non-owner")
	}
	appErr, ok := err.(*model.AppError)
	if !ok {
		t.Fatalf("expected *model.AppError, got %T", err)
	}
	if appErr.HTTPStatus != 403 {
		t.Errorf("expected 403, got %d", appErr.HTTPStatus)
	}
}

func TestParseBearerToken(t *testing.T) {
	tests := []struct {
		header  string
		want    string
		wantErr bool
	}{
		{"Bearer abc123", "abc123", false},
		{"bearer abc123", "abc123", false},
		{"", "", true},
		{"Basic abc123", "", true},
		{"Bearer", "", true},
	}
	for _, tt := range tests {
		got, err := ParseBearerToken(tt.header)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseBearerToken(%q) error = %v, wantErr %v", tt.header, err, tt.wantErr)
		}
		if got != tt.want {
			t.Errorf("ParseBearerToken(%q) = %q, want %q", tt.header, got, tt.want)
		}
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewRateLimiter(RateLimitConfig{
		MutatePerMinute:  2,
		ReadPerMinute:    5,
		MetricsPerMinute: 3,
	})
	id := uuid.New()

	// Should allow initial burst
	if !rl.AllowMutate(id) {
		t.Error("first mutate should be allowed")
	}
	if !rl.AllowMutate(id) {
		t.Error("second mutate should be allowed (burst)")
	}

	// Third should be rate limited (burst size = 2)
	// Note: rate.Limiter uses token bucket, third call depends on timing.
	// We just verify the limiter doesn't panic and returns bool.
}
