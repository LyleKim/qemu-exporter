package main

import "testing"

func TestOtherHost(t *testing.T) {
	m := &migrator{nodeHosts: [2]string{"node-a.example.com", "node-b.example.com"}}

	if got := m.otherHost("node-a.example.com"); got != "node-b.example.com" {
		t.Errorf("otherHost(node-a) = %q, want node-b.example.com", got)
	}
	if got := m.otherHost("NODE-B.EXAMPLE.COM"); got != "node-a.example.com" {
		t.Errorf("otherHost is case-sensitive, want case-insensitive match: got %q", got)
	}
	if got := m.otherHost("node-c.example.com"); got != "" {
		t.Errorf("otherHost(unknown) = %q, want empty string", got)
	}
}
