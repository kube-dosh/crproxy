// Package tlsutil provides atomic TLS certificate reloading for long-running
// servers. A failed reload keeps the previous certificate active.
package tlsutil

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type Reloader struct {
	certFile  string
	keyFile   string
	logger    *slog.Logger
	active    atomic.Pointer[tls.Certificate]
	mu        sync.Mutex
	attempt   [sha256.Size]byte
	attempted bool
}

func NewReloader(certFile, keyFile string, logger *slog.Logger) (*Reloader, error) {
	if certFile == "" || keyFile == "" {
		return nil, errors.New("TLS certificate and key files are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	reloader := &Reloader{certFile: certFile, keyFile: keyFile, logger: logger}
	if err := reloader.Reload(); err != nil {
		return nil, err
	}
	return reloader, nil
}

func (r *Reloader) Reload() error {
	hash, err := certificateFilesHash(r.certFile, r.keyFile)
	if err != nil {
		return err
	}
	return r.load(hash)
}

func (r *Reloader) load(hash [sha256.Size]byte) error {
	r.mu.Lock()
	r.attempt = hash
	r.attempted = true
	r.mu.Unlock()
	certificate, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("load TLS certificate: %w", err)
	}
	if len(certificate.Certificate) == 0 {
		return errors.New("TLS certificate chain is empty")
	}
	r.active.Store(&certificate)
	r.logger.Info("TLS certificate loaded", "cert_file", r.certFile)
	return nil
}

func (r *Reloader) reloadIfChanged() error {
	hash, err := certificateFilesHash(r.certFile, r.keyFile)
	if err != nil {
		return err
	}
	r.mu.Lock()
	unchanged := r.attempted && hash == r.attempt
	r.mu.Unlock()
	if unchanged {
		return nil
	}
	return r.load(hash)
}

func certificateFilesHash(certFile, keyFile string) ([sha256.Size]byte, error) {
	hash := sha256.New()
	for _, path := range []string{certFile, keyFile} {
		raw, err := os.ReadFile(path)
		if err != nil {
			return [sha256.Size]byte{}, fmt.Errorf("read TLS material %s: %w", path, err)
		}
		_, _ = hash.Write(raw)
		_, _ = hash.Write([]byte{0})
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func (r *Reloader) Config(minVersion uint16) *tls.Config {
	if minVersion == 0 {
		minVersion = tls.VersionTLS12
	}
	return &tls.Config{
		MinVersion: minVersion,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			certificate := r.active.Load()
			if certificate == nil {
				return nil, errors.New("TLS certificate is not loaded")
			}
			return certificate, nil
		},
	}
}

func (r *Reloader) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.reloadIfChanged(); err != nil {
				r.logger.Error("TLS certificate reload failed; keeping previous certificate", "error", err)
			}
		}
	}
}
