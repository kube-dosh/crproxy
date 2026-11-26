package oci

import "testing"

func TestParseOCIManifest(t *testing.T) {
	raw := []byte(`{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "config": {"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824","size":2},
  "layers": []
}`)
	manifest, err := ParseManifest(raw, MediaTypeOCIManifest)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.IsIndex() {
		t.Fatal("manifest unexpectedly treated as index")
	}
	if manifest.ContentKind() != "manifest" {
		t.Fatalf("unexpected kind %q", manifest.ContentKind())
	}
	if got := len(manifest.ReferencedDescriptors()); got != 1 {
		t.Fatalf("expected 1 referenced descriptor, got %d", got)
	}
}

func TestParseManifestRejectsMismatchedMediaType(t *testing.T) {
	raw := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824","size":2}}`)
	if _, err := ParseManifest(raw, MediaTypeOCIIndex); err == nil {
		t.Fatal("expected media type mismatch")
	}
}

func TestParseManifestRejectsMissingConfig(t *testing.T) {
	raw := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","layers":[]}`)
	if _, err := ParseManifest(raw, MediaTypeOCIManifest); err == nil {
		t.Fatal("expected missing config error")
	}
}
