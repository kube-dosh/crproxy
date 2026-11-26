package id

import (
	"strings"
	"testing"
)

func TestNewProducesPrefixedUniqueIdentifiers(t *testing.T) {
	first := New("job_", 12)
	second := New("job_", 12)
	if !strings.HasPrefix(first, "job_") || len(first) != len("job_")+24 {
		t.Fatalf("unexpected identifier %q", first)
	}
	if first == second {
		t.Fatal("identifiers must be unique")
	}
	if value := New("usr_", 0); !strings.HasPrefix(value, "usr_") || len(value) != len("usr_")+32 {
		t.Fatalf("non-positive byte count should use the safe default, got %q", value)
	}
}
