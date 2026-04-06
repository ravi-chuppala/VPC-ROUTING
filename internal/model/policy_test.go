package model

import (
	"net/netip"
	"testing"
)

func TestEvaluatePrefix(t *testing.T) {
	tests := []struct {
		name         string
		policy       RoutePolicy
		prefix       string
		currentCount int
		wantDecision PolicyDecision
		wantReason   string
	}{
		{
			name:         "default policy accepts all",
			policy:       DefaultRoutePolicy(),
			prefix:       "10.0.0.0/16",
			currentCount: 0,
			wantDecision: PolicyAccept,
		},
		{
			name: "denied prefix is rejected",
			policy: RoutePolicy{
				DeniedPrefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/16")},
				MaxPrefixes:    100,
			},
			prefix:       "10.0.0.0/16",
			currentCount: 0,
			wantDecision: PolicyDeny,
			wantReason:   "prefix matches denied list",
		},
		{
			name: "denied supernet blocks subnet",
			policy: RoutePolicy{
				DeniedPrefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/16")},
				MaxPrefixes:    100,
			},
			prefix:       "10.0.1.0/24",
			currentCount: 0,
			wantDecision: PolicyDeny,
		},
		{
			name: "allowed list accepts matching prefix",
			policy: RoutePolicy{
				AllowedPrefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/16")},
				MaxPrefixes:     100,
			},
			prefix:       "10.0.1.0/24",
			currentCount: 0,
			wantDecision: PolicyAccept,
		},
		{
			name: "allowed list filters non-matching prefix",
			policy: RoutePolicy{
				AllowedPrefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/16")},
				MaxPrefixes:     100,
			},
			prefix:       "10.1.0.0/16",
			currentCount: 0,
			wantDecision: PolicyFiltered,
			wantReason:   "prefix not in allowed list",
		},
		{
			name: "denied wins over allowed",
			policy: RoutePolicy{
				AllowedPrefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/16")},
				DeniedPrefixes:  []netip.Prefix{netip.MustParsePrefix("10.0.1.0/24")},
				MaxPrefixes:     100,
			},
			prefix:       "10.0.1.0/24",
			currentCount: 0,
			wantDecision: PolicyDeny,
		},
		{
			name: "max prefix at limit rejects",
			policy: RoutePolicy{
				MaxPrefixes: 5,
			},
			prefix:       "10.0.0.0/16",
			currentCount: 5,
			wantDecision: PolicyFiltered,
			wantReason:   "max_prefix_limit_reached",
		},
		{
			name: "max prefix below limit accepts",
			policy: RoutePolicy{
				MaxPrefixes: 5,
			},
			prefix:       "10.0.0.0/16",
			currentCount: 4,
			wantDecision: PolicyAccept,
		},
		{
			name: "null allowed prefixes accepts all",
			policy: RoutePolicy{
				AllowedPrefixes: nil,
				MaxPrefixes:     100,
			},
			prefix:       "192.168.0.0/16",
			currentCount: 0,
			wantDecision: PolicyAccept,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix := netip.MustParsePrefix(tt.prefix)
			decision, reason := EvaluatePrefix(tt.policy, prefix, tt.currentCount)
			if decision != tt.wantDecision {
				t.Errorf("EvaluatePrefix() decision = %v, want %v", decision, tt.wantDecision)
			}
			if tt.wantReason != "" && reason != tt.wantReason {
				t.Errorf("EvaluatePrefix() reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}
