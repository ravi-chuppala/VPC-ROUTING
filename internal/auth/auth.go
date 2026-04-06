package auth

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/ravi-chuppala/vpc-routing/internal/model"
)

type contextKey string

const accountKey contextKey = "account_id"

// ContextWithAccount returns a context with the account ID set.
func ContextWithAccount(ctx context.Context, accountID uuid.UUID) context.Context {
	return context.WithValue(ctx, accountKey, accountID)
}

// AccountFromContext extracts the account ID from the context.
func AccountFromContext(ctx context.Context) (uuid.UUID, error) {
	v := ctx.Value(accountKey)
	if v == nil {
		return uuid.Nil, model.ErrUnauthenticated()
	}
	id, ok := v.(uuid.UUID)
	if !ok {
		return uuid.Nil, model.ErrUnauthenticated()
	}
	return id, nil
}

// RequireVPCOwner checks that the caller owns the VPC.
func RequireVPCOwner(ctx context.Context, vpc *model.VPC) error {
	accountID, err := AccountFromContext(ctx)
	if err != nil {
		return err
	}
	if vpc.AccountID != accountID {
		return model.ErrPermissionDenied("caller does not own this VPC")
	}
	return nil
}

// RequirePeeringAccess checks that the caller owns at least one side of the peering.
func RequirePeeringAccess(ctx context.Context, peering *model.Peering, requesterAccountID, accepterAccountID uuid.UUID) error {
	accountID, err := AccountFromContext(ctx)
	if err != nil {
		return err
	}
	if accountID != requesterAccountID && accountID != accepterAccountID {
		return model.ErrPermissionDenied("caller does not own either VPC in this peering")
	}
	return nil
}

// ParseBearerToken extracts the token from an "Authorization: Bearer <token>" header.
func ParseBearerToken(header string) (string, error) {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", model.ErrUnauthenticated()
	}
	return parts[1], nil
}
