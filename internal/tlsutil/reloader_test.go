package tlsutil

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReloadKeepsPreviousCertificateOnFailure(t *testing.T) {
	directory := t.TempDir()
	certPath := filepath.Join(directory, "tls.crt")
	keyPath := filepath.Join(directory, "tls.key")
	writeCertificate(t, certPath, keyPath, "first.example")
	reloader, err := NewReloader(certPath, keyPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := reloader.Config(tls.VersionTLS12).GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}
	writeCertificate(t, certPath, keyPath, "second.example")
	if err := reloader.Reload(); err != nil {
		t.Fatal(err)
	}
	second, err := reloader.Config(tls.VersionTLS12).GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Certificate[0]) == string(second.Certificate[0]) {
		t.Fatal("certificate did not reload")
	}
	if err := os.WriteFile(certPath, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reloader.Reload(); err == nil {
		t.Fatal("broken certificate reload should fail")
	}
	kept, err := reloader.Config(tls.VersionTLS12).GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if string(kept.Certificate[0]) != string(second.Certificate[0]) {
		t.Fatal("failed reload must keep previous certificate")
	}
}

func TestRunStopsWithContext(t *testing.T) {
	directory := t.TempDir()
	certPath := filepath.Join(directory, "tls.crt")
	keyPath := filepath.Join(directory, "tls.key")
	writeCertificate(t, certPath, keyPath, "run.example")
	reloader, err := NewReloader(certPath, keyPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { reloader.Run(ctx, 10*time.Millisecond); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reloader did not stop on context cancellation")
	}
}

func TestRunOnlyLoadsChangedCertificate(t *testing.T) {
	directory := t.TempDir()
	certPath := filepath.Join(directory, "tls.crt")
	keyPath := filepath.Join(directory, "tls.key")
	writeCertificate(t, certPath, keyPath, "stable.example")
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	reloader, err := NewReloader(certPath, keyPath, logger)
	if err != nil {
		t.Fatal(err)
	}
	runFor := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		defer cancel()
		reloader.Run(ctx, 10*time.Millisecond)
	}
	runFor()
	if count := strings.Count(logs.String(), "TLS certificate loaded"); count != 1 {
		t.Fatalf("unchanged certificate should be loaded once, got %d logs: %s", count, logs.String())
	}
	writeCertificate(t, certPath, keyPath, "rotated.example")
	runFor()
	if count := strings.Count(logs.String(), "TLS certificate loaded"); count != 2 {
		t.Fatalf("changed certificate should be loaded once more, got %d logs: %s", count, logs.String())
	}
	if err := os.WriteFile(certPath, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFor()
	runFor()
	if count := strings.Count(logs.String(), "reload failed"); count != 1 {
		t.Fatalf("unchanged broken certificate should log one error, got %d: %s", count, logs.String())
	}
	if certificate, err := reloader.Config(tls.VersionTLS12).GetCertificate(&tls.ClientHelloInfo{}); err != nil || certificate == nil {
		t.Fatalf("previous certificate should remain active: %v", err)
	}
}

func writeCertificate(t *testing.T, certPath, keyPath, commonName string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: commonName}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{commonName}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
}
