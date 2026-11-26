package oci

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"regexp"
	"strings"
)

var digestAlgorithmPattern = regexp.MustCompile(`^[a-z0-9]+$`)
var digestHexPattern = regexp.MustCompile(`^[a-f0-9]+$`)

// Digest is an OCI content digest in the form algorithm:hex.
type Digest struct {
	Algorithm string
	Encoded   string
}

func ParseDigest(value string) (Digest, error) {
	algorithm, encoded, ok := strings.Cut(value, ":")
	if !ok || algorithm == "" || encoded == "" {
		return Digest{}, fmt.Errorf("invalid digest")
	}
	if !digestAlgorithmPattern.MatchString(algorithm) || !digestHexPattern.MatchString(encoded) {
		return Digest{}, fmt.Errorf("invalid digest")
	}

	var expected int
	switch algorithm {
	case "sha256":
		expected = sha256.Size * 2
	case "sha512":
		expected = sha512.Size * 2
	default:
		return Digest{}, fmt.Errorf("unsupported digest algorithm %q", algorithm)
	}
	if len(encoded) != expected {
		return Digest{}, fmt.Errorf("invalid %s digest length", algorithm)
	}
	return Digest{Algorithm: algorithm, Encoded: encoded}, nil
}

func (d Digest) String() string {
	return d.Algorithm + ":" + d.Encoded
}

func (d Digest) Validate() error {
	if d.Algorithm == "" || d.Encoded == "" {
		return fmt.Errorf("empty digest")
	}
	if _, err := ParseDigest(d.String()); err != nil {
		return err
	}
	return nil
}

func NewHash(algorithm string) (hash.Hash, error) {
	switch algorithm {
	case "sha256":
		return sha256.New(), nil
	case "sha512":
		return sha512.New(), nil
	default:
		return nil, fmt.Errorf("unsupported digest algorithm %q", algorithm)
	}
}

func FromBytes(algorithm string, data []byte) (Digest, error) {
	h, err := NewHash(algorithm)
	if err != nil {
		return Digest{}, err
	}
	_, _ = h.Write(data)
	return Digest{Algorithm: algorithm, Encoded: hex.EncodeToString(h.Sum(nil))}, nil
}

func FromReader(algorithm string, r io.Reader) (Digest, int64, error) {
	h, err := NewHash(algorithm)
	if err != nil {
		return Digest{}, 0, err
	}
	n, err := io.Copy(h, r)
	if err != nil {
		return Digest{}, n, err
	}
	return Digest{Algorithm: algorithm, Encoded: hex.EncodeToString(h.Sum(nil))}, n, nil
}

func VerifyReader(algorithm string, expected Digest, r io.Reader) (int64, error) {
	actual, n, err := FromReader(algorithm, r)
	if err != nil {
		return n, err
	}
	if actual != expected {
		return n, fmt.Errorf("digest mismatch: expected %s, got %s", expected, actual)
	}
	return n, nil
}
