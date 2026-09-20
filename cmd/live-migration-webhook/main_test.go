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

func TestConstantTimeBearerMatch(t *testing.T) {
	cases := []struct {
		name   string
		header string
		secret string
		want   bool
	}{
		{"valid bearer", "Bearer s3cr3t", "s3cr3t", true},
		{"wrong secret", "Bearer wrong", "s3cr3t", false},
		{"missing prefix", "s3cr3t", "s3cr3t", false},
		{"empty header", "", "s3cr3t", false},
		{"basic auth scheme", "Basic czNjcjN0", "s3cr3t", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := constantTimeBearerMatch(c.header, c.secret); got != c.want {
				t.Errorf("constantTimeBearerMatch(%q, %q) = %v, want %v", c.header, c.secret, got, c.want)
			}
		})
	}
}

func TestInstanceUUIDRe(t *testing.T) {
	valid := "5101f966-2bab-483d-807c-87ff8732f2ab"
	if !instanceUUIDRe.MatchString(valid) {
		t.Errorf("instanceUUIDRe rejected valid uuid %q", valid)
	}
	invalid := []string{"", "not-a-uuid", "5101f966-2bab-483d-807c-87ff8732f2ab/../action", "5101f966-2bab-483d-807c-87ff8732f2ab "}
	for _, v := range invalid {
		if instanceUUIDRe.MatchString(v) {
			t.Errorf("instanceUUIDRe accepted invalid value %q", v)
		}
	}
}
