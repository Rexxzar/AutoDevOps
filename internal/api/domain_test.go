package api

import "testing"

func TestValidateDomain(t *testing.T) {
	valid := []string{
		"",                      // empty = no custom domain, allowed
		"tripulse.example.com", // the correct one
		"app.example.com",
		"a.b.co",
		"apps.127.0.0.1.sslip.io", // dev wildcard domain (numeric labels)
		"my-app.sub.example.co.uk",
	}
	for _, d := range valid {
		if err := validateDomain(d); err != nil {
			t.Errorf("validateDomain(%q) = %v, want nil", d, err)
		}
	}

	invalid := []string{
		"front.example..com", // the real bug: double dot
		".example.com",        // leading dot
		"example.com.",        // trailing dot
		"no-dot",              // single label, no dot
		"a b.com",             // space
		"-bad.example.com",    // leading hyphen label
		"bad-.example.com",    // trailing hyphen label
	}
	for _, d := range invalid {
		if err := validateDomain(d); err == nil {
			t.Errorf("validateDomain(%q) = nil, want error", d)
		}
	}
}
