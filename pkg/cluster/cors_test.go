package cluster

import "testing"

func TestMatchCORSOrigin(t *testing.T) {
	tests := []struct {
		allowed string
		origin  string
		want    string
	}{
		{"*", "https://example.com", "https://example.com"},
		{"*", "", "*"},
		{"", "https://example.com", "https://example.com"},
		{"https://foo.com, https://bar.com", "https://bar.com", "https://bar.com"},
		{"https://foo.com, https://bar.com", "https://baz.com", ""},
		{"https://foo.com", "https://foo.com/", "https://foo.com/"},
		{"https://foo.com, *", "https://any.com", "https://any.com"},
		{"example.com", "https://example.com", "https://example.com"},
		{"example.com", "https://www.example.com", "https://www.example.com"},
		{"example.com", "https://badsite.com", ""},
	}

	for _, tc := range tests {
		got := MatchCORSOrigin(tc.allowed, tc.origin)
		if got != tc.want {
			t.Errorf("MatchCORSOrigin(%q, %q) = %q; want %q", tc.allowed, tc.origin, got, tc.want)
		}
	}
}
