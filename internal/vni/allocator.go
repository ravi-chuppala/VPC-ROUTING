package vni

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/google/uuid"
)

const (
	minVNI      = 4096      // VNI 1-4095 reserved (overlaps VLAN IDs)
	maxVNI      = 16000000  // VNI 16M+ reserved for future use
	regionBits  = 4         // top 4 bits = region ID
	tenantBits  = 10        // next 10 bits = tenant shard
	vpcBits     = 10        // bottom 10 bits = VPC sequence
)

// Allocator manages VNI allocation for VPCs.
type Allocator struct {
	mu        sync.Mutex
	allocated map[uint32]bool
	regionMap map[string]uint32 // region name -> region ID (0-15)
}

func NewAllocator() *Allocator {
	return &Allocator{
		allocated: make(map[uint32]bool),
		regionMap: make(map[string]uint32),
	}
}

// RegisterRegion maps a region name to a region ID (0-15).
func (a *Allocator) RegisterRegion(name string, id uint32) error {
	if id > 15 {
		return fmt.Errorf("region ID must be 0-15, got %d", id)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.regionMap[name] = id
	return nil
}

// Allocate assigns a VNI for a VPC in the given region and account.
func (a *Allocator) Allocate(_ context.Context, regionID string, accountID uuid.UUID) (uint32, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	region, ok := a.regionMap[regionID]
	if !ok {
		return 0, fmt.Errorf("unknown region: %s", regionID)
	}

	tenantShard := accountShard(accountID)

	// Try each VPC sequence in the tenant's shard first
	for seq := uint32(0); seq < (1 << vpcBits); seq++ {
		vni := EncodeVNI(region, tenantShard, seq)
		if vni < minVNI || vni >= maxVNI {
			continue
		}
		if !a.allocated[vni] {
			a.allocated[vni] = true
			return vni, nil
		}
	}

	// Fallback: scan all shards in this region for any free VNI
	for shard := uint32(0); shard < (1 << tenantBits); shard++ {
		for seq := uint32(0); seq < (1 << vpcBits); seq++ {
			vni := EncodeVNI(region, shard, seq)
			if vni < minVNI || vni >= maxVNI {
				continue
			}
			if !a.allocated[vni] {
				a.allocated[vni] = true
				return vni, nil
			}
		}
	}

	return 0, fmt.Errorf("VNI pool exhausted for region %s", regionID)
}

// Release marks a VNI as available.
func (a *Allocator) Release(_ context.Context, vni uint32) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.allocated, vni)
	return nil
}

// IsAllocated checks if a VNI is currently in use.
func (a *Allocator) IsAllocated(vni uint32) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.allocated[vni]
}

// EncodeVNI creates a VNI from region, tenant shard, and VPC sequence.
func EncodeVNI(region, tenantShard, vpcSeq uint32) uint32 {
	return (region << (tenantBits + vpcBits)) | (tenantShard << vpcBits) | vpcSeq
}

// DecodeVNI extracts region, tenant shard, and VPC sequence from a VNI.
func DecodeVNI(vni uint32) (region, tenantShard, vpcSeq uint32) {
	vpcSeq = vni & ((1 << vpcBits) - 1)
	tenantShard = (vni >> vpcBits) & ((1 << tenantBits) - 1)
	region = (vni >> (tenantBits + vpcBits)) & ((1 << regionBits) - 1)
	return
}

// accountShard hashes an account UUID to a 10-bit shard (0-1023).
func accountShard(accountID uuid.UUID) uint32 {
	h := sha256.Sum256(accountID[:])
	return binary.BigEndian.Uint32(h[:4]) & ((1 << tenantBits) - 1)
}
