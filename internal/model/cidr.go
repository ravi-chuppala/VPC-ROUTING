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
func ValidateCIDR(cidr netip.Prefix) error {
	if !cidr.IsValid() {
		return fmt.Errorf("invalid CIDR: %s", cidr)
	}
	if !cidr.Addr().Is4() {
		return fmt.Errorf("only IPv4 CIDRs are supported: %s", cidr)
	}
	bits := cidr.Bits()
	if bits < 16 || bits > 28 {
		return fmt.Errorf("CIDR prefix length must be between /16 and /28, got /%d", bits)
	}
	if !isRFC1918(cidr) {
		return fmt.Errorf("CIDR must be an RFC 1918 private range: %s", cidr)
	}
	return nil
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
	return a.Contains(b.Addr()) || b.Contains(a.Addr())
}

// ValidateCIDRBlocks validates a slice of CIDRs: 1-5 blocks, all valid, no internal overlap.
func ValidateCIDRBlocks(cidrs []netip.Prefix) error {
	if len(cidrs) == 0 || len(cidrs) > 5 {
		return fmt.Errorf("must provide 1 to 5 CIDR blocks, got %d", len(cidrs))
	}
	for _, c := range cidrs {
		if err := ValidateCIDR(c); err != nil {
			return err
		}
	}
	// Check internal overlap
	for i := 0; i < len(cidrs); i++ {
		for j := i + 1; j < len(cidrs); j++ {
			if prefixesOverlap(cidrs[i], cidrs[j]) {
				return fmt.Errorf("CIDR blocks overlap: %s and %s", cidrs[i], cidrs[j])
			}
		}
	}
	return nil
}
