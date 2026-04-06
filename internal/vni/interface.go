package vni

import (
	"context"

	"github.com/google/uuid"
)

// VNIAllocator is the interface for VNI allocation/release.
type VNIAllocator interface {
	Allocate(ctx context.Context, regionID string, accountID uuid.UUID) (uint32, error)
	Release(ctx context.Context, vni uint32) error
}

// Verify Allocator implements VNIAllocator at compile time.
var _ VNIAllocator = (*Allocator)(nil)
