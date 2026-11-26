package oci

import (
	"encoding/json"
	"fmt"
	"mime"
	"strings"
)

const (
	MediaTypeOCIManifest     = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeOCIIndex        = "application/vnd.oci.image.index.v1+json"
	MediaTypeDockerManifest  = "application/vnd.docker.distribution.manifest.v2+json"
	MediaTypeDockerList      = "application/vnd.docker.distribution.manifest.list.v2+json"
	MediaTypeDockerConfig    = "application/vnd.docker.container.image.v1+json"
	MediaTypeOCIConfig       = "application/vnd.oci.image.config.v1+json"
	MediaTypeOCILayer        = "application/vnd.oci.image.layer.v1.tar+gzip"
	MediaTypeOCILayerNonDist = "application/vnd.oci.image.layer.nondistributable.v1.tar+gzip"
)

type Platform struct {
	Architecture string `json:"architecture,omitempty"`
	OS           string `json:"os,omitempty"`
	OSVersion    string `json:"os.version,omitempty"`
	Variant      string `json:"variant,omitempty"`
}

type Descriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	URLs         []string          `json:"urls,omitempty"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	Platform     *Platform         `json:"platform,omitempty"`
	Data         string            `json:"data,omitempty"`
}

type Manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType,omitempty"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Config        *Descriptor       `json:"config,omitempty"`
	Layers        []Descriptor      `json:"layers,omitempty"`
	Manifests     []Descriptor      `json:"manifests,omitempty"`
	Subject       *Descriptor       `json:"subject,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`

	Raw []byte `json:"-"`
}

func SupportedManifestMediaType(mediaType string) bool {
	switch mediaType {
	case MediaTypeOCIManifest, MediaTypeOCIIndex, MediaTypeDockerManifest, MediaTypeDockerList:
		return true
	default:
		return false
	}
}

func ParseManifest(raw []byte, contentType string) (*Manifest, error) {
	contentType = strings.TrimSpace(contentType)
	if contentType != "" {
		if parsed, _, err := mime.ParseMediaType(contentType); err == nil {
			contentType = parsed
		}
	}

	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("invalid manifest json: %w", err)
	}
	manifest.Raw = raw
	if manifest.SchemaVersion != 2 {
		return nil, fmt.Errorf("unsupported schema version %d", manifest.SchemaVersion)
	}
	if manifest.MediaType == "" {
		manifest.MediaType = contentType
	}
	if !SupportedManifestMediaType(manifest.MediaType) {
		return nil, fmt.Errorf("unsupported manifest media type %q", manifest.MediaType)
	}
	if contentType != "" && SupportedManifestMediaType(contentType) && contentType != manifest.MediaType {
		return nil, fmt.Errorf("content-type and manifest mediaType differ")
	}

	switch manifest.MediaType {
	case MediaTypeOCIManifest, MediaTypeDockerManifest:
		if manifest.Config == nil {
			return nil, fmt.Errorf("manifest config is required")
		}
		if err := validateDescriptor(*manifest.Config); err != nil {
			return nil, fmt.Errorf("invalid config descriptor: %w", err)
		}
		for i := range manifest.Layers {
			if err := validateDescriptor(manifest.Layers[i]); err != nil {
				return nil, fmt.Errorf("invalid layer descriptor %d: %w", i, err)
			}
		}
	case MediaTypeOCIIndex, MediaTypeDockerList:
		for i := range manifest.Manifests {
			if err := validateDescriptor(manifest.Manifests[i]); err != nil {
				return nil, fmt.Errorf("invalid child manifest descriptor %d: %w", i, err)
			}
		}
	}
	if manifest.Subject != nil {
		if err := validateDescriptor(*manifest.Subject); err != nil {
			return nil, fmt.Errorf("invalid subject descriptor: %w", err)
		}
	}
	return &manifest, nil
}

func validateDescriptor(descriptor Descriptor) error {
	if descriptor.MediaType == "" {
		return fmt.Errorf("mediaType is required")
	}
	if descriptor.Size < 0 {
		return fmt.Errorf("size must not be negative")
	}
	if _, err := ParseDigest(descriptor.Digest); err != nil {
		return err
	}
	return nil
}

func (m *Manifest) IsIndex() bool {
	return m.MediaType == MediaTypeOCIIndex || m.MediaType == MediaTypeDockerList
}

func (m *Manifest) ContentKind() string {
	if m.IsIndex() {
		return "index"
	}
	return "manifest"
}

func (m *Manifest) EffectiveArtifactType() string {
	if m.ArtifactType != "" {
		return m.ArtifactType
	}
	if m.Config != nil {
		return m.Config.MediaType
	}
	return ""
}

func (m *Manifest) ReferencedDescriptors() []Descriptor {
	if m.IsIndex() {
		return m.Manifests
	}
	result := make([]Descriptor, 0, 1+len(m.Layers))
	if m.Config != nil {
		result = append(result, *m.Config)
	}
	result = append(result, m.Layers...)
	return result
}

func (m *Manifest) Digest(algorithm string) (Digest, error) {
	return FromBytes(algorithm, m.Raw)
}
