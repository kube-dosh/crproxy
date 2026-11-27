package runtimeconfig

import (
	"errors"
	"fmt"
	"time"
)

const (
	SectionRegistryAuth = "registry-auth"
	SectionWebSession   = "web-session"
	SectionStorage      = "storage"
	SectionUploads      = "uploads"
	SectionAudit        = "audit"
	SectionProxy        = "proxy"
	SectionOutbound     = "outbound-proxy"
	SectionSecurity     = "security"
)

var (
	ErrInvalid        = errors.New("invalid runtime configuration")
	ErrUnknownSection = errors.New("unknown runtime configuration section")
)

// ConflictError means a section was changed by another administrator after it
// was loaded by the caller.
type ConflictError struct {
	Section        string
	CurrentVersion int64
	UpdatedAt      *time.Time
	UpdatedBy      string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("runtime configuration section %s has version %d", e.Section, e.CurrentVersion)
}

type RegistryAuth struct {
	Challenge        string   `json:"challenge"`
	Service          string   `json:"service"`
	Realm            string   `json:"realm"`
	AllowAnonymous   bool     `json:"allowAnonymous"`
	AnonymousActions []string `json:"anonymousActions"`
	TokenTTL         string   `json:"tokenTtl"`
}

type WebSession struct {
	SessionTTL  string `json:"sessionTtl"`
	IdleTimeout string `json:"idleTimeout"`
}

type Storage struct {
	MaxBytes            int64   `json:"maxBytes"`
	CacheMaxBytes       int64   `json:"cacheMaxBytes"`
	MinFreeBytes        int64   `json:"minFreeBytes"`
	HighWatermark       float64 `json:"highWatermark"`
	LowWatermark        float64 `json:"lowWatermark"`
	OnFull              string  `json:"onFull"`
	MaxBlobBytes        int64   `json:"maxBlobBytes"`
	MaxManifestBytes    int64   `json:"maxManifestBytes"`
	DownloadConcurrency int     `json:"downloadConcurrency"`
	TmpTTL              string  `json:"tmpTtl"`
	TrashTTL            string  `json:"trashTtl"`
}

type Uploads struct {
	UploadsTTL string `json:"uploadsTtl"`
}

type Audit struct {
	AuditRetention string `json:"auditRetention"`
}

type Proxy struct {
	Disabled bool `json:"disabled"`
}

type Security struct {
	HSTS bool `json:"hsts"`
}

type OutboundProxy struct {
	HTTPProxy  string `json:"httpProxy"`
	HTTPSProxy string `json:"httpsProxy"`
	NoProxy    string `json:"noProxy"`
	Username   string `json:"username"`
	Password   string `json:"-"`
}

type OutboundProxyInput struct {
	HTTPProxy     string `json:"httpProxy"`
	HTTPSProxy    string `json:"httpsProxy"`
	NoProxy       string `json:"noProxy"`
	Username      string `json:"username"`
	Password      string `json:"password,omitempty"`
	ClearPassword bool   `json:"clearPassword,omitempty"`
}

type OutboundProxyView struct {
	HTTPProxy          string `json:"httpProxy"`
	HTTPSProxy         string `json:"httpsProxy"`
	NoProxy            string `json:"noProxy"`
	Username           string `json:"username"`
	PasswordConfigured bool   `json:"passwordConfigured"`
	PasswordSource     string `json:"passwordSource"`
}

type Startup struct {
	Listen                   string   `json:"listen"`
	PublicHost               string   `json:"publicHost"`
	PublicScheme             string   `json:"publicScheme"`
	RegistryURL              string   `json:"registryURL"`
	AdminListen              string   `json:"adminListen"`
	AdminPublicHost          string   `json:"adminPublicHost"`
	AdminPublicScheme        string   `json:"adminPublicScheme"`
	AdminURL                 string   `json:"adminURL"`
	PlaneMode                string   `json:"planeMode"`
	TrustedProxies           []string `json:"trustedProxies"`
	TLSMode                  string   `json:"tlsMode"`
	TLSMinVersion            string   `json:"tlsMinVersion"`
	TLSReloadSeconds         int      `json:"tlsReloadSeconds"`
	TLSCertificateConfigured bool     `json:"tlsCertificateConfigured"`
	TLSKeyConfigured         bool     `json:"tlsKeyConfigured"`
	StorageRoot              string   `json:"storageRoot"`
	ProxyPathPrefix          string   `json:"proxyPathPrefix"`
	BootstrapAdminUsername   string   `json:"bootstrapAdminUsername"`
	JWTSecretConfigured      bool     `json:"jwtSecretConfigured"`
	MasterKeyConfigured      bool     `json:"masterKeyConfigured"`
}

// Base is the immutable startup baseline. Runtime section overrides are layered
// on top of it.
type Base struct {
	RegistryAuth RegistryAuth
	WebSession   WebSession
	Storage      Storage
	Uploads      Uploads
	Audit        Audit
	Proxy        Proxy
	Outbound     OutboundProxy
	Security     Security
	Startup      Startup
}

type SectionMeta struct {
	Version   int64      `json:"version"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	UpdatedBy string     `json:"updatedBy,omitempty"`
	Source    string     `json:"source"`
}

// Config is the effective immutable snapshot. Callers must not mutate it.
type Config struct {
	RegistryAuth RegistryAuth
	WebSession   WebSession
	Storage      Storage
	Uploads      Uploads
	Audit        Audit
	Proxy        Proxy
	Outbound     OutboundProxy
	Security     Security
	Startup      Startup
	Sections     map[string]SectionMeta
}

type View struct {
	RegistryAuth  RegistryAuth           `json:"registryAuth"`
	WebSession    WebSession             `json:"webSession"`
	Storage       Storage                `json:"storage"`
	Uploads       Uploads                `json:"uploads"`
	Audit         Audit                  `json:"audit"`
	Proxy         Proxy                  `json:"proxy"`
	OutboundProxy OutboundProxyView      `json:"outboundProxy"`
	Security      Security               `json:"security"`
	Startup       Startup                `json:"startup"`
	Sections      map[string]SectionMeta `json:"sections"`
}

func (c Config) View() View {
	passwordSource := "none"
	if c.Outbound.Password != "" {
		passwordSource = "startup"
		if meta := c.Sections[SectionOutbound]; meta.Source == "runtime" {
			passwordSource = "runtime"
		}
	}
	return View{
		RegistryAuth: c.RegistryAuth,
		WebSession:   c.WebSession,
		Storage:      c.Storage,
		Uploads:      c.Uploads,
		Audit:        c.Audit,
		Proxy:        c.Proxy,
		OutboundProxy: OutboundProxyView{
			HTTPProxy:          c.Outbound.HTTPProxy,
			HTTPSProxy:         c.Outbound.HTTPSProxy,
			NoProxy:            c.Outbound.NoProxy,
			Username:           c.Outbound.Username,
			PasswordConfigured: c.Outbound.Password != "",
			PasswordSource:     passwordSource,
		},
		Security: c.Security,
		Startup:  c.Startup,
		Sections: cloneMeta(c.Sections),
	}
}

func cloneMeta(input map[string]SectionMeta) map[string]SectionMeta {
	output := make(map[string]SectionMeta, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
