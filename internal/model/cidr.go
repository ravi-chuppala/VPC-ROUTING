package model

import (
	"fmt"
	"net/netip"
)

var rfc1918Ranges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

// ValidateCIDR checks that a CIDR is a valid RFC 1918 range with prefix length /16 to /28.
// It normalizes the prefix to canonical form (host bits masked).
func ValidateCIDR(cidr netip.Prefix) (netip.Prefix, error) {
	if !cidr.IsValid() {
		return cidr, fmt.Errorf("invalid CIDR: %s", cidr)
	}
	// Normalize to canonical form (e.g., 10.0.1.0/16 → 10.0.0.0/16)
	cidr = cidr.Masked()
	if !cidr.Addr().Is4() {
		return cidr, fmt.Errorf("only IPv4 CIDRs are supported: %s", cidr)
	}
	bits := cidr.Bits()
	if bits < 16 || bits > 28 {
		return cidr, fmt.Errorf("CIDR prefix length must be between /16 and /28, got /%d", bits)
	}
	if !isRFC1918(cidr) {
		return cidr, fmt.Errorf("CIDR must be an RFC 1918 private range: %s", cidr)
	}
	return cidr, nil
}

func isRFC1918(cidr netip.Prefix) bool {
	for _, r := range rfc1918Ranges {
		if r.Contains(cidr.Addr()) {
			return true
		}
	}
	return false
}

// CIDRsOverlap returns true if any prefix in a overlaps with any prefix in b.
func CIDRsOverlap(a, b []netip.Prefix) bool {
	for _, pa := range a {
		for _, pb := range b {
			if prefixesOverlap(pa, pb) {
				return true
			}
		}
	}
	return false
}

// prefixesOverlap returns true if two prefixes share any IP addresses.
func prefixesOverlap(a, b netip.Prefix) bool {
	return a.Overlaps(b)
}

// ValidateCIDRBlocks validates a slice of CIDRs: 1-5 blocks, all valid, no internal overlap.
// Returns the normalized (masked) CIDRs.
func ValidateCIDRBlocks(cidrs []netip.Prefix) ([]netip.Prefix, error) {
	if len(cidrs) == 0 || len(cidrs) > 5 {
		return nil, fmt.Errorf("must provide 1 to 5 CIDR blocks, got %d", len(cidrs))
	}
	normalized := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		n, err := ValidateCIDR(c)
		if err != nil {
			return nil, err
		}
		normalized[i] = n
	}
	// Check internal overlap
	for i := 0; i < len(normalized); i++ {
		for j := i + 1; j < len(normalized); j++ {
			if prefixesOverlap(normalized[i], normalized[j]) {
				return nil, fmt.Errorf("CIDR blocks overlap: %s and %s", normalized[i], normalized[j])
			}
		}
	}
	return normalized, nil
}
