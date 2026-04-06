package model

import (
	"net/netip"
	"testing"
)

func TestValidateCIDR(t *testing.T) {
	tests := []struct {
		name    string
		cidr    string
		wantErr bool
	}{
		{"valid /16", "10.0.0.0/16", false},
		{"valid /24", "10.0.1.0/24", false},
		{"valid /28", "10.0.0.0/28", false},
		{"valid 172.16", "172.16.0.0/16", false},
		{"valid 192.168", "192.168.0.0/16", false},
		{"too broad /15", "10.0.0.0/15", true},
		{"too narrow /29", "10.0.0.0/29", true},
		{"public IP", "8.8.8.0/24", true},
		{"IPv6", "fd00::/64", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := netip.ParsePrefix(tt.cidr)
			if err != nil {
				if !tt.wantErr {
					t.Fatalf("failed to parse prefix: %v", err)
				}
				return
			}
			_, err = ValidateCIDR(p)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCIDR(%s) error = %v, wantErr %v", tt.cidr, err, tt.wantErr)
			}
		})
	}
}

func TestCIDRsOverlap(t *testing.T) {
	tests := []struct {
		name string
		a    []string
		b    []string
		want bool
	}{
		{
			"no overlap",
			[]string{"10.0.0.0/16"},
			[]string{"10.1.0.0/16"},
			false,
		},
		{
			"exact match",
			[]string{"10.0.0.0/16"},
			[]string{"10.0.0.0/16"},
			true,
		},
		{
			"supernet contains subnet",
			[]string{"10.0.0.0/16"},
			[]string{"10.0.1.0/24"},
			true,
		},
		{
			"subnet in supernet",
			[]string{"10.0.1.0/24"},
			[]string{"10.0.0.0/16"},
			true,
		},
		{
			"different ranges no overlap",
			[]string{"10.0.0.0/16", "10.1.0.0/16"},
			[]string{"10.2.0.0/16"},
			false,
		},
		{
			"multi with overlap",
			[]string{"10.0.0.0/16", "10.2.0.0/16"},
			[]string{"10.2.0.0/24"},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := parsePrefixes(t, tt.a)
			b := parsePrefixes(t, tt.b)
			got := CIDRsOverlap(a, b)
			if got != tt.want {
				t.Errorf("CIDRsOverlap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateCIDRBlocks(t *testing.T) {
	tests := []struct {
		name    string
		cidrs   []string
		wantErr bool
	}{
		{"single valid", []string{"10.0.0.0/16"}, false},
		{"five valid", []string{"10.0.0.0/16", "10.1.0.0/16", "10.2.0.0/16", "10.3.0.0/16", "10.4.0.0/16"}, false},
		{"empty", []string{}, true},
		{"six blocks", []string{"10.0.0.0/16", "10.1.0.0/16", "10.2.0.0/16", "10.3.0.0/16", "10.4.0.0/16", "10.5.0.0/16"}, true},
		{"internal overlap", []string{"10.0.0.0/16", "10.0.1.0/24"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cidrs := parsePrefixes(t, tt.cidrs)
			_, err := ValidateCIDRBlocks(cidrs)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCIDRBlocks() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func parsePrefixes(t *testing.T, strs []string) []netip.Prefix {
	t.Helper()
	result := make([]netip.Prefix, len(strs))
	for i, s := range strs {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", s, err)
		}
		result[i] = p
	}
	return result
}
