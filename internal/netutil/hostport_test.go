package netutil

import "testing"

func TestValidateHostPort(t *testing.T) {
	valid := []string{"localhost", "localhost:5000", "registry.example.com", "registry.example.com:443", "127.0.0.1:5000", "[::1]:5000"}
	for _, value := range valid {
		if err := ValidateHostPort(value); err != nil {
			t.Errorf("ValidateHostPort(%q) failed: %v", value, err)
		}
	}
	invalid := []string{"", "https://registry.example.com", "registry.example.com/v2", "registry.example.com:0", "registry.example.com:65536", "registry.example.com:bad", "user@registry.example.com", "bad_host", "registry..example.com", "-registry.example.com"}
	for _, value := range invalid {
		if err := ValidateHostPort(value); err == nil {
			t.Errorf("ValidateHostPort(%q) should fail", value)
		}
	}
}
