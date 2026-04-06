package model

import "net/netip"

type RoutePolicy struct {
	AllowedPrefixes    []netip.Prefix `json:"allowed_prefixes"`
	DeniedPrefixes     []netip.Prefix `json:"denied_prefixes"`
	MaxPrefixes        int            `json:"max_prefixes"`
	BandwidthLimitMbps *int           `json:"bandwidth_limit_mbps"`
}

func DefaultRoutePolicy() RoutePolicy {
	return RoutePolicy{
		MaxPrefixes: 100,
	}
}

type PolicyDecision string

const (
	PolicyAccept   PolicyDecision = "accept"
	PolicyDeny     PolicyDecision = "deny"
	PolicyFiltered PolicyDecision = "filtered"
)

// EvaluatePrefix checks whether a prefix should be accepted per the route policy.
// Evaluation order per FR-5.2: denied -> allowed -> max_prefixes -> accept.
func EvaluatePrefix(policy RoutePolicy, prefix netip.Prefix, currentCount int) (PolicyDecision, string) {
	// Step 1: Check denied prefixes
	for _, denied := range policy.DeniedPrefixes {
		if prefixContains(denied, prefix) {
			return PolicyDeny, "prefix matches denied list"
		}
	}

	// Step 2: Check allowed prefixes (whitelist mode)
	if len(policy.AllowedPrefixes) > 0 {
		found := false
		for _, allowed := range policy.AllowedPrefixes {
			if prefixContains(allowed, prefix) {
				found = true
				break
			}
		}
		if !found {
			return PolicyFiltered, "prefix not in allowed list"
		}
	}

	// Step 3: Check max-prefix limit
	if policy.MaxPrefixes > 0 && currentCount >= policy.MaxPrefixes {
		return PolicyFiltered, "max_prefix_limit_reached"
	}

	return PolicyAccept, ""
}

// prefixContains returns true if outer contains or equals inner.
func prefixContains(outer, inner netip.Prefix) bool {
	if outer == inner {
		return true
	}
	// outer contains inner if outer's bits <= inner's bits and outer contains inner's addr
	return outer.Bits() <= inner.Bits() && outer.Contains(inner.Addr())
}
