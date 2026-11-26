package oci

import (
	"strings"
	"testing"
)

func TestDigestRoundTrip(t *testing.T) {
	digest, err := FromBytes("sha256", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if digest.String() != "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("unexpected digest %s", digest)
	}
	parsed, err := ParseDigest(digest.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed != digest {
		t.Fatalf("round trip mismatch: %#v != %#v", parsed, digest)
	}
}

func TestDigestRejectsInvalidValues(t *testing.T) {
	values := []string{
		"",
		"sha256",
		"sha256:",
		"sha256:abc",
		"sha1:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
		"sha256:2CF24DBA5FB0A30E26E83B2AC5B9E29E1B161E5C1FA7425E73043362938B9824",
		"sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824x",
	}
	for _, value := range values {
		if _, err := ParseDigest(value); err == nil {
			t.Errorf("expected %q to be rejected", value)
		}
	}
}

func TestDigestFromReader(t *testing.T) {
	digest, size, err := FromReader("sha256", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if size != 5 {
		t.Fatalf("unexpected size %d", size)
	}
	if digest.Encoded != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("unexpected digest %s", digest)
	}
}
