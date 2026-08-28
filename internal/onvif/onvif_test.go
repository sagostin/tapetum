package onvif

import (
	"testing"
)

func TestCIDRHosts(t *testing.T) {
	tests := []struct {
		cidr   string
		want   []string
		errSub string
	}{
		{
			cidr: "192.168.1.0/24",
			want: []string{
				"192.168.1.1", "192.168.1.2", "192.168.1.3", "192.168.1.4",
				"192.168.1.5", "192.168.1.6", "192.168.1.7", "192.168.1.8",
			},
		},
		{
			cidr: "192.168.1.0/30",
			want: []string{"192.168.1.1", "192.168.1.2"},
		},
		{
			cidr: "192.168.1.0/31",
			want: []string{"192.168.1.0", "192.168.1.1"},
		},
		{
			cidr: "10.0.0.5/32",
			want: []string{"10.0.0.5"},
		},
		{
			cidr:   "not-a-cidr",
			errSub: "invalid CIDR",
		},
		{
			cidr:   "::1/128",
			errSub: "IPv4",
		},
		{
			cidr:   "10.0.0.0/8",
			errSub: "too large",
		},
	}
	for _, tc := range tests {
		t.Run(tc.cidr, func(t *testing.T) {
			got, err := CIDRHosts(tc.cidr)
			if tc.errSub != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (got=%v)", tc.errSub, got)
				}
				if !contains(err.Error(), tc.errSub) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.errSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) > 8 {
				got = got[:8]
			}
			if len(tc.want) > 8 {
				tc.want = tc.want[:8]
			}
			if !equalStrings(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveHost(t *testing.T) {
	// IP literal.
	got, err := ResolveHost("127.0.0.1")
	if err != nil {
		t.Fatalf("localhost literal: %v", err)
	}
	if len(got) != 1 || got[0] != "127.0.0.1" {
		t.Fatalf("literal: got %v", got)
	}
	// IPv6 literal rejected.
	if _, err := ResolveHost("::1"); err == nil {
		t.Fatalf("expected IPv6 rejection")
	}
	// Bogus hostname.
	if _, err := ResolveHost("definitely-not-a-real-host-xyz123.invalid"); err == nil {
		t.Fatalf("expected resolve failure")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
