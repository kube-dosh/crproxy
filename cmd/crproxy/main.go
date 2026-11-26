package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"crproxy/internal/adminapi"
	"crproxy/internal/auth"
	"crproxy/internal/backup"
	"crproxy/internal/cas"
	"crproxy/internal/config"
	"crproxy/internal/jobs"
	"crproxy/internal/observability"
	"crproxy/internal/proxy"
	"crproxy/internal/registry"
	"crproxy/internal/requestinfo"
	"crproxy/internal/secretbox"
	"crproxy/internal/storage"
	"crproxy/internal/store"
	"crproxy/internal/tlsutil"
	"crproxy/internal/upstreammgr"
	"crproxy/web"
)

var version = "0.1.0-dev"

func main() {
	var err error
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "backup":
			err = runBackup(args[1:])
		case "restore":
			err = runRestore(args[1:])
		case "rotate-master-key":
			err = runRotateMasterKey(args[1:])
		case "healthcheck":
			err = runHealthcheck(args[1:])
		case "version":
			fmt.Println(version)
			return
		case "serve":
			err = run(args[1:])
		default:
			err = run(args)
		}
	} else {
		err = run(nil)
	}
	if err != nil {
		slog.Error("crproxy stopped", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("crproxy", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to crproxy YAML configuration")
	showVersion := flags.Bool("version", false, "print version and exit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println(version)
		return nil
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	startedAt := time.Now().UTC()

	root, err := filepath.Abs(cfg.Storage.Root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, "meta"), 0o750); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	lock, err := acquireLock(filepath.Join(root, "LOCK"))
	if err != nil {
		return err
	}
	defer lock.Close()

	casStore, err := cas.New(root)
	if err != nil {
		return fmt.Errorf("initialize CAS: %w", err)
	}
	db, err := store.Open(context.Background(), filepath.Join(root, "meta", "crproxy.db"))
	if err != nil {
		return fmt.Errorf("open metadata database: %w", err)
	}
	defer db.Close()
	metadata := store.New(db)

	ctx := context.Background()
	if err := bootstrapAdmin(ctx, metadata, cfg, logger); err != nil {
		return err
	}
	secret, err := loadJWTSecret(cfg, root)
	if err != nil {
		return err
	}
	ttl, err := cfg.TokenTTL()
	if err != nil {
		return err
	}
	signer, err := auth.NewSigner(secret, "crproxy", cfg.Auth.Registry.Service, ttl)
	if err != nil {
		return err
	}
	authenticator := &auth.Authenticator{
		Store:          metadata,
		Signer:         signer,
		Realm:          cfg.Realm(),
		Service:        cfg.Auth.Registry.Service,
		ChallengeMode:  cfg.Auth.Registry.Challenge,
		AllowAnonymous: cfg.Auth.Registry.AllowAnonymous,
		DummyHash:      auth.DummyPasswordHash(),
	}
	webSessionTTL, err := cfg.WebSessionTTL()
	if err != nil {
		return err
	}
	webIdleTimeout, err := cfg.WebIdleTimeout()
	if err != nil {
		return err
	}
	resolver, err := requestinfo.New(cfg.Server.TrustedProxies, cfg.PublicScheme())
	if err != nil {
		return fmt.Errorf("initialize request origin resolver: %w", err)
	}
	secureCookies := cfg.PublicScheme() == "https"
	if !secureCookies {
		logger.Warn("management sessions use a non-Secure cookie because HTTP is enabled; terminate TLS before production use")
	}
	uploadTTL, err := cfg.UploadTTL()
	if err != nil {
		return err
	}
	tmpTTL, err := cfg.Duration("storage.tmp_ttl", cfg.Storage.TmpTTL)
	if err != nil {
		return err
	}
	trashTTL, err := cfg.Duration("storage.trash_ttl", cfg.Storage.TrashTTL)
	if err != nil {
		return err
	}
	auditRetention, err := cfg.Duration("storage.audit_retention", cfg.Storage.AuditRetention)
	if err != nil {
		return err
	}
	storageManager, err := storage.New(metadata, casStore, storage.Config{
		Root: root,
		Settings: storage.Settings{
			MaxBytes:      cfg.Storage.MaxBytes,
			CacheMaxBytes: cfg.Storage.CacheMaxBytes,
			MinFreeBytes:  cfg.Storage.MinFreeBytes,
			HighWatermark: cfg.Storage.HighWatermark,
			LowWatermark:  cfg.Storage.LowWatermark,
			OnFull:        cfg.Storage.OnFull,
		},
		TmpTTL:              tmpTTL,
		TrashTTL:            trashTTL,
		DownloadConcurrency: cfg.Storage.DownloadConcurrency,
	}, logger)
	if err != nil {
		return err
	}
	if err := storageManager.Recover(ctx); err != nil {
		return fmt.Errorf("recover storage state: %w", err)
	}
	jobRunner, err := jobs.New(metadata, logger)
	if err != nil {
		return err
	}
	if err := jobRunner.RecoverInterrupted(ctx); err != nil {
		return fmt.Errorf("recover interrupted jobs: %w", err)
	}
	registryHandler := &registry.Handler{
		Store:            metadata,
		CAS:              casStore,
		Auth:             authenticator,
		MaxManifestBytes: cfg.Storage.MaxManifestBytes,
		MaxBlobBytes:     cfg.Storage.MaxBlobBytes,
		UploadTTL:        uploadTTL,
		Storage:          storageManager,
		Logger:           logger,
	}
	tokenHandler := &registry.TokenHandler{Auth: authenticator, Logger: logger}
	proxyHandler, err := proxy.NewHandler(registryHandler, metadata, casStore, authenticator, nil, cfg.Proxy.PathPrefix, cfg.Storage.MaxManifestBytes, logger)
	if err != nil {
		return err
	}
	proxyHandler.Storage = storageManager
	masterKeyPath := cfg.Auth.MasterKeyFile
	if masterKeyPath == "" {
		masterKeyPath = filepath.Join(root, "meta", "master.key")
	}
	masterKeyring, err := secretbox.LoadOrCreateKeyring(masterKeyPath)
	if err != nil {
		return fmt.Errorf("load upstream master key: %w", err)
	}
	upstreamManager, err := upstreammgr.NewWithKeyProvider(metadata, proxyHandler, masterKeyring, logger)
	if err != nil {
		return err
	}
	if err := seedUpstreams(ctx, upstreamManager, cfg); err != nil {
		return err
	}
	if err := upstreamManager.Reload(ctx); err != nil {
		return fmt.Errorf("load upstreams: %w", err)
	}
	adminAPI, err := adminapi.NewHandler(metadata, authenticator, adminapi.Config{
		CSRFSecret:      secret,
		SessionTTL:      webSessionTTL,
		SessionIdleTTL:  webIdleTimeout,
		SecureCookies:   secureCookies,
		Version:         version,
		StartedAt:       startedAt,
		Upstreams:       upstreamManager,
		Jobs:            jobRunner,
		Storage:         storageManager,
		CAS:             casStore,
		AuditRetention:  auditRetention,
		RequestInfo:     resolver,
		PublicURL:       cfg.PublicScheme() + "://" + cfg.Server.PublicHost,
		ProxyPathPrefix: cfg.Proxy.PathPrefix,
		AllowAnonymous:  cfg.Auth.Registry.AllowAnonymous,
	}, logger)
	if err != nil {
		return err
	}
	metricsCollector, err := observability.New(metadata, storageManager, upstreamManager, version, startedAt)
	if err != nil {
		return fmt.Errorf("initialize metrics collector: %w", err)
	}
	var registryFrontend http.Handler = registryHandler
	if !cfg.Proxy.Disabled {
		registryFrontend = proxyHandler
	}
	mux := http.NewServeMux()
	mux.Handle("/v2/token", tokenHandler)
	mux.Handle("/v2/", registryFrontend)
	mux.Handle("/api/v1/", adminAPI)
	mux.HandleFunc("GET /api/v1/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","version":"` + version + `"}`))
	})
	mux.HandleFunc("GET /api/v1/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, readyCancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer readyCancel()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := metadata.DB.PingContext(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not_ready","component":"database"}`))
			return
		}
		if _, err := storageManager.Summary(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not_ready","component":"storage"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready","version":"` + version + `"}`))
	})
	mux.Handle("/metrics", metricsCollector)
	mux.Handle("/", web.Handler())

	server := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           recoverMiddleware(logger, requestLogMiddleware(logger, securityHeadersMiddleware(resolver, cfg.Server.TLS.HSTS, metricsCollector.Instrument(mux)))),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	runCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	listener, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Server.Listen, err)
	}
	defer listener.Close()
	go cleanupUploads(runCtx, metadata, casStore, logger)
	go cleanupSessions(runCtx, metadata, logger)
	go storageMaintenance(runCtx, storageManager, auditRetention, logger)
	go func() {
		<-runCtx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
		}
	}()

	logger.Info("crproxy starting",
		"version", version,
		"listen", cfg.Server.Listen,
		"public_host", cfg.Server.PublicHost,
		"storage_root", root,
		"tls", cfg.Server.TLS.Mode,
		"public_scheme", cfg.PublicScheme(),
		"trusted_proxies", len(cfg.Server.TrustedProxies),
		"allow_anonymous", cfg.Auth.Registry.AllowAnonymous,
		"upstreams", len(cfg.Upstreams),
		"master_key", masterKeyPath,
		"master_key_version", masterKeyring.CurrentVersion(),
	)
	switch cfg.Server.TLS.Mode {
	case "":
		err = server.Serve(listener)
	case "file":
		reloader, reloadErr := tlsutil.NewReloader(cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile, logger)
		if reloadErr != nil {
			return reloadErr
		}
		go reloader.Run(runCtx, time.Duration(cfg.Server.TLS.ReloadSeconds)*time.Second)
		err = server.Serve(tls.NewListener(listener, reloader.Config(tlsMinVersion(cfg.Server.TLS.MinVersion))))
	default:
		return fmt.Errorf("unsupported server.tls.mode %q", cfg.Server.TLS.Mode)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func runBackup(args []string) error {
	flags := flag.NewFlagSet("crproxy backup", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to crproxy YAML configuration")
	output := flags.String("output", "", "backup archive path (default: timestamped tar.gz in current directory)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(cfg.Storage.Root)
	if err != nil {
		return err
	}
	if *output == "" {
		*output = "crproxy-backup-" + time.Now().UTC().Format("20060102T150405Z") + ".tar.gz"
	}
	lock, err := acquireLock(filepath.Join(root, "LOCK"))
	if err != nil {
		return err
	}
	defer lock.Close()
	result, err := backup.Create(root, *output, version)
	if err != nil {
		return err
	}
	fmt.Printf("backup created: %s\nsha256: %s\nfiles: %d\nbytes: %d\n", result.Output, result.SHA256, result.Files, result.TotalBytes)
	return nil
}

func runRestore(args []string) error {
	flags := flag.NewFlagSet("crproxy restore", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to crproxy YAML configuration")
	input := flags.String("input", "", "backup archive path")
	force := flags.Bool("force", false, "preserve and replace a non-empty data directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return errors.New("restore requires -input")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(cfg.Storage.Root)
	if err != nil {
		return err
	}
	var lock *os.File
	if info, statErr := os.Stat(root); statErr == nil && info.IsDir() {
		lock, err = acquireLock(filepath.Join(root, "LOCK"))
		if err != nil {
			return err
		}
		defer lock.Close()
	}
	if err := backup.Restore(*input, root, *force); err != nil {
		return err
	}
	fmt.Printf("backup restored to %s\n", root)
	return nil
}

func runRotateMasterKey(args []string) error {
	flags := flag.NewFlagSet("crproxy rotate-master-key", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to crproxy YAML configuration")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(cfg.Storage.Root)
	if err != nil {
		return err
	}
	lock, err := acquireLock(filepath.Join(root, "LOCK"))
	if err != nil {
		return err
	}
	defer lock.Close()
	keyPath := cfg.Auth.MasterKeyFile
	if keyPath == "" {
		keyPath = filepath.Join(root, "meta", "master.key")
	}
	keyring, err := secretbox.LoadOrCreateKeyring(keyPath)
	if err != nil {
		return err
	}
	db, err := store.Open(context.Background(), filepath.Join(root, "meta", "crproxy.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	metadata := store.New(db)
	result, err := upstreammgr.RotateMasterKey(context.Background(), metadata, keyring)
	if err != nil {
		return err
	}
	_ = metadata.AddAuditEvent(context.Background(), store.AuditEvent{
		ActorType: "system", Action: "master_key.rotate", TargetType: "master_key", TargetID: keyPath, Result: "success",
	})
	fmt.Printf("master key rotated: previous=%d current=%d generated=%t rewrapped=%d\n", result.PreviousVersion, result.CurrentVersion, result.GeneratedKey, result.Rewrapped)
	return nil
}

func runHealthcheck(args []string) error {
	flags := flag.NewFlagSet("crproxy healthcheck", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to crproxy YAML configuration")
	endpoint := flags.String("url", "", "readiness endpoint (default: <public scheme>://<public host>/api/v1/readyz)")
	timeout := flags.Duration("timeout", 5*time.Second, "request timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *timeout <= 0 {
		return errors.New("healthcheck timeout must be positive")
	}
	target := strings.TrimSpace(*endpoint)
	if target == "" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}
		target = cfg.PublicScheme() + "://" + cfg.Server.PublicHost + "/api/v1/readyz"
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("build healthcheck request: %w", err)
	}
	client := &http.Client{
		Timeout: *timeout,
		Transport: &http.Transport{
			Proxy: nil,
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("healthcheck request failed: %w", err)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
	if readErr != nil {
		return fmt.Errorf("read healthcheck response: %w", readErr)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("healthcheck failed: status=%d body=%s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	fmt.Printf("healthy: %s\n", strings.TrimSpace(string(body)))
	return nil
}

func tlsMinVersion(value string) uint16 {
	if value == "1.3" {
		return tls.VersionTLS13
	}
	return tls.VersionTLS12
}

func securityHeadersMiddleware(resolver *requestinfo.Resolver, hsts bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if hsts && resolver.Scheme(r) == "https" {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func seedUpstreams(ctx context.Context, manager *upstreammgr.Manager, cfg config.Config) error {
	for _, item := range cfg.Upstreams {
		if _, err := manager.Get(ctx, item.ID); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("read seed upstream %s: %w", item.ID, err)
		}
		maxBlob := item.MaxBlobBytes
		caPEM := item.CAPEM
		if caPEM == "" && item.CAFile != "" {
			raw, err := os.ReadFile(item.CAFile)
			if err != nil {
				return fmt.Errorf("read upstream %s CA file: %w", item.ID, err)
			}
			caPEM = string(raw)
		}
		_, err := manager.Upsert(ctx, upstreammgr.UpsertInput{
			ID:                 item.ID,
			Name:               item.Name,
			Endpoint:           item.Endpoint,
			RouteHost:          item.RouteHost,
			RoutePrefix:        item.RoutePrefix,
			RepositoryPrefix:   item.RepositoryPrefix,
			AuthType:           item.AuthType,
			CAPEM:              caPEM,
			InsecureSkipVerify: item.InsecureSkipVerify,
			TagTTLSeconds:      int64(item.TagTTLDuration().Seconds()),
			RequestTimeoutSecs: int64(item.RequestTimeoutDuration().Seconds()),
			MaxBlobBytes:       &maxBlob,
			Enabled:            !item.Disabled,
			ReplaceCredentials: true,
			Credentials: upstreammgr.Credentials{
				Username:    item.Username,
				Password:    item.Password,
				BearerToken: item.BearerToken,
			},
		})
		if err != nil {
			return fmt.Errorf("seed upstream %s: %w", item.ID, err)
		}
	}
	return nil
}

func acquireLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open data directory lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("data directory is already in use: %w", err)
	}
	return file, nil
}

func bootstrapAdmin(ctx context.Context, metadata *store.Store, cfg config.Config, logger *slog.Logger) error {
	count, err := metadata.CountUsers(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	password := cfg.Auth.BootstrapAdmin.Password
	if len(password) < 12 {
		return errors.New("bootstrap admin password is missing or shorter than 12 characters; set CRPROXY_ADMIN_PASSWORD")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	user, err := metadata.CreateUserWithPasswordChange(ctx, cfg.Auth.BootstrapAdmin.Username, hash, "admin", true)
	if err != nil {
		return fmt.Errorf("create bootstrap administrator: %w", err)
	}
	if err := metadata.AddAuditEvent(ctx, store.AuditEvent{
		ActorType:  "system",
		Action:     "user.bootstrap",
		TargetType: "user",
		TargetID:   user.ID,
		Result:     "success",
	}); err != nil {
		return fmt.Errorf("audit bootstrap administrator: %w", err)
	}
	logger.Info("created bootstrap administrator", "username", user.Username)
	return nil
}

func loadJWTSecret(cfg config.Config, root string) ([]byte, error) {
	path := cfg.Auth.JWTSecretFile
	if path == "" {
		path = filepath.Join(root, "meta", "jwt.secret")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		if len(raw) < 32 {
			return nil, fmt.Errorf("JWT secret file %s must contain at least 32 bytes", path)
		}
		return raw, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(secret); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return secret, nil
}

func cleanupUploads(ctx context.Context, metadata *store.Store, casStore *cas.Store, logger *slog.Logger) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ids, err := metadata.ExpireUploads(ctx, time.Now().UTC())
			if err != nil {
				logger.Error("expire uploads failed", "error", err)
				continue
			}
			for _, uploadID := range ids {
				if err := casStore.DeleteUpload(uploadID); err != nil {
					logger.Warn("remove expired upload file failed", "upload_id", uploadID, "error", err)
				}
			}
		}
	}
}

func cleanupSessions(ctx context.Context, metadata *store.Store, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := metadata.DeleteExpiredSessions(ctx, time.Now().UTC()); err != nil {
				logger.Warn("delete expired sessions failed", "error", err)
			}
		}
	}
}

func storageMaintenance(ctx context.Context, manager *storage.Manager, auditRetention time.Duration, logger *slog.Logger) {
	gcTicker := time.NewTicker(15 * time.Minute)
	retentionTicker := time.NewTicker(6 * time.Hour)
	defer gcTicker.Stop()
	defer retentionTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-gcTicker.C:
			if _, err := manager.GC(ctx, false); err != nil {
				logger.Warn("background storage GC failed", "error", err)
			}
		case <-retentionTicker.C:
			if err := manager.EnforceRetention(ctx, auditRetention); err != nil {
				logger.Warn("enforce event retention failed", "error", err)
			}
		}
	}
}

func requestLogMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("http request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr, "duration_ms", time.Since(start).Milliseconds())
	})
}

func recoverMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				logger.Error("http handler panic", "panic", value, "path", r.URL.Path)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
