package vni

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func newTestAllocator(t *testing.T) *Allocator {
	t.Helper()
	a := NewAllocator()
	a.RegisterRegion("us-east-1", 0)
	a.RegisterRegion("us-west-1", 1)
	a.RegisterRegion("eu-west-1", 2)
	return a
}

func TestAllocateAndRelease(t *testing.T) {
	a := newTestAllocator(t)
	ctx := context.Background()
	accountID := uuid.New()

	vni1, err := a.Allocate(ctx, "us-east-1", accountID)
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	if vni1 < 4096 || vni1 >= 16000000 {
		t.Errorf("VNI %d is in reserved range", vni1)
	}

	// Second allocation should produce different VNI
	vni2, err := a.Allocate(ctx, "us-east-1", accountID)
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	if vni1 == vni2 {
		t.Error("two allocations produced same VNI")
	}

	// Release and re-allocate
	if err := a.Release(ctx, vni1); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if a.IsAllocated(vni1) {
		t.Error("VNI should not be allocated after release")
	}
}

func TestEncodeDecodeVNI(t *testing.T) {
	tests := []struct {
		region, tenant, vpc uint32
	}{
		{0, 0, 0},
		{1, 100, 500},
		{15, 1023, 1023},
		{2, 512, 256},
	}
	for _, tt := range tests {
		vni := EncodeVNI(tt.region, tt.tenant, tt.vpc)
		r, te, v := DecodeVNI(vni)
		if r != tt.region || te != tt.tenant || v != tt.vpc {
			t.Errorf("EncodeVNI(%d,%d,%d) = %d, DecodeVNI = (%d,%d,%d)",
				tt.region, tt.tenant, tt.vpc, vni, r, te, v)
		}
	}
}

func TestReservedRanges(t *testing.T) {
	a := newTestAllocator(t)
	ctx := context.Background()

	// Allocate many VNIs, none should be in reserved ranges
	for i := 0; i < 100; i++ {
		vni, err := a.Allocate(ctx, "us-east-1", uuid.New())
		if err != nil {
			t.Fatalf("Allocate() error on iteration %d: %v", i, err)
		}
		if vni < 4096 {
			t.Errorf("VNI %d is below reserved minimum 4096", vni)
		}
		if vni >= 16000000 {
			t.Errorf("VNI %d is in reserved upper range", vni)
		}
	}
}

func TestConcurrentAllocation(t *testing.T) {
	a := newTestAllocator(t)
	ctx := context.Background()

	const n = 100
	vnis := make([]uint32, n)
	var wg sync.WaitGroup
	var mu sync.Mutex
	errCount := 0

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			vni, err := a.Allocate(ctx, "us-east-1", uuid.New())
			if err != nil {
				mu.Lock()
				errCount++
				mu.Unlock()
				return
			}
			mu.Lock()
			vnis[idx] = vni
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	if errCount > 0 {
		t.Errorf("%d allocations failed", errCount)
	}

	// Check uniqueness
	seen := make(map[uint32]bool)
	for _, v := range vnis {
		if v == 0 {
			continue
		}
		if seen[v] {
			t.Errorf("duplicate VNI: %d", v)
		}
		seen[v] = true
	}
}

func TestUnknownRegion(t *testing.T) {
	a := NewAllocator()
	_, err := a.Allocate(context.Background(), "unknown", uuid.New())
	if err == nil {
		t.Error("expected error for unknown region")
	}
}
