package model

import (
	"net/netip"
	"time"

	"github.com/google/uuid"
)

type VPCState string

const (
	VPCStateActive   VPCState = "active"
	VPCStateDeleting VPCState = "deleting"
	VPCStateDeleted  VPCState = "deleted"
)

type VPC struct {
	ID           uuid.UUID
	AccountID    uuid.UUID
	RegionID     string
	Name         string
	CIDRBlocks   []netip.Prefix
	VNI          uint32
	VRFName      string
	RD           string // Route Distinguisher
	ExportRT     string // Export Route Target
	State        VPCState
	PeeringCount int
	CreatedAt    time.Time
	DeletedAt    *time.Time
}
