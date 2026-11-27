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
	"crproxy/internal/runtimeconfig"
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
	masterKeyPath := cfg.Auth.MasterKeyFile
	if masterKeyPath == "" {
		masterKeyPath = filepath.Join(root, "meta", "master.key")
	}
	masterKeyring, err := secretbox.LoadOrCreateKeyring(masterKeyPath)
	if err != nil {
		return fmt.Errorf("load upstream master key: %w", err)
	}
	runtimeManager, err := runtimeconfig.New(ctx, metadata, runtimeBaseFromConfig(cfg, root), masterKeyring)
	if err != nil {
		return fmt.Errorf("load runtime configuration: %w", err)
	}
	runtimeCurrent := runtimeManager.Current()
	if runtimeCurrent == nil {
		return errors.New("runtime configuration is not initialized")
	}
	secret, err := loadJWTSecret(cfg, root)
	if err != nil {
		return err
	}
	signer, err := auth.NewSigner(secret, "crproxy", runtimeCurrent.RegistryAuth.Service, runtimeCurrent.TokenTTLDuration())
	if err != nil {
		return err
	}
	authenticator := &auth.Authenticator{
		Store:            metadata,
		Signer:           signer,
		Realm:            runtimeCurrent.RegistryAuth.Realm,
		Service:          runtimeCurrent.RegistryAuth.Service,
		ChallengeMode:    runtimeCurrent.RegistryAuth.Challenge,
		AllowAnonymous:   runtimeCurrent.RegistryAuth.AllowAnonymous,
		AnonymousActions: append([]string(nil), runtimeCurrent.RegistryAuth.AnonymousActions...),
		DummyHash:        auth.DummyPasswordHash(),
	}
	webSessionTTL, webIdleTimeout := runtimeCurrent.WebSessionDurations()
	registryResolver, err := requestinfo.New(cfg.Server.TrustedProxies, cfg.PublicScheme())
	if err != nil {
		return fmt.Errorf("initialize registry request origin resolver: %w", err)
	}
	adminResolver, err := requestinfo.New(cfg.Server.TrustedProxies, cfg.AdminPublicScheme())
	if err != nil {
		return fmt.Errorf("initialize management request origin resolver: %w", err)
	}
	secureCookies := cfg.AdminPublicScheme() == "https"
	if !secureCookies {
		logger.Warn("management sessions use a non-Secure cookie because HTTP is enabled; terminate TLS before production use")
	}
	uploadTTL := runtimeCurrent.UploadTTLDuration()
	tmpTTL, trashTTL := runtimeCurrent.StorageDurations()
	auditRetention := runtimeCurrent.AuditRetentionDuration()
	storageManager, err := storage.New(metadata, casStore, storage.Config{
		Root: root,
		Settings: storage.Settings{
			MaxBytes:      runtimeCurrent.Storage.MaxBytes,
			CacheMaxBytes: runtimeCurrent.Storage.CacheMaxBytes,
			MinFreeBytes:  runtimeCurrent.Storage.MinFreeBytes,
			HighWatermark: runtimeCurrent.Storage.HighWatermark,
			LowWatermark:  runtimeCurrent.Storage.LowWatermark,
			OnFull:        runtimeCurrent.Storage.OnFull,
		},
		TmpTTL:              tmpTTL,
		TrashTTL:            trashTTL,
		DownloadConcurrency: runtimeCurrent.Storage.DownloadConcurrency,
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
		MaxManifestBytes: runtimeCurrent.Storage.MaxManifestBytes,
		MaxBlobBytes:     runtimeCurrent.Storage.MaxBlobBytes,
		UploadTTL:        uploadTTL,
		Storage:          storageManager,
		Logger:           logger,
	}
	tokenHandler := &registry.TokenHandler{Auth: authenticator, Logger: logger}
	proxyHandler, err := proxy.NewHandler(registryHandler, metadata, casStore, authenticator, nil, cfg.Proxy.PathPrefix, runtimeCurrent.Storage.MaxManifestBytes, logger)
	if err != nil {
		return err
	}
	proxyHandler.Storage = storageManager
	upstreamManager, err := upstreammgr.NewWithKeyProvider(metadata, proxyHandler, masterKeyring, logger)
	if err != nil {
		return err
	}
	if err := upstreamManager.SetOutboundProxy(outboundProxyFromRuntime(runtimeCurrent.Outbound)); err != nil {
		return fmt.Errorf("configure outbound proxy: %w", err)
	}
	if err := seedUpstreams(ctx, upstreamManager, cfg); err != nil {
		return err
	}
	if err := upstreamManager.Reload(ctx); err != nil {
		return fmt.Errorf("load upstreams: %w", err)
	}
	adminAPI, err := adminapi.NewHandler(metadata, adminapi.Config{
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
		RequestInfo:     adminResolver,
		PublicURL:       cfg.AdminPublicURL(),
		RegistryURL:     cfg.RegistryPublicURL(),
		DeploymentMode:  cfg.ListenerMode(),
		RegistryListen:  cfg.Server.Listen,
		AdminListen:     cfg.AdminListen(),
		ProxyPathPrefix: cfg.Proxy.PathPrefix,
		AllowAnonymous:  runtimeCurrent.RegistryAuth.AllowAnonymous,
		Runtime:         runtimeManager,
	}, logger)
	if err != nil {
		return err
	}
	runtimeManager.SetApply(func(current runtimeconfig.Config) {
		if err := authenticator.ApplyPolicy(auth.RegistryPolicy{
			Realm:            current.RegistryAuth.Realm,
			Service:          current.RegistryAuth.Service,
			ChallengeMode:    current.RegistryAuth.Challenge,
			AllowAnonymous:   current.RegistryAuth.AllowAnonymous,
			AnonymousActions: current.RegistryAuth.AnonymousActions,
			TokenTTL:         current.TokenTTLDuration(),
		}); err != nil {
			logger.Error("apply registry auth policy failed", "error", err)
		}
		registryHandler.ApplyRuntimeLimits(registry.RuntimeLimits{
			MaxManifestBytes: current.Storage.MaxManifestBytes,
			MaxBlobBytes:     current.Storage.MaxBlobBytes,
			UploadTTL:        current.UploadTTLDuration(),
		})
		proxyHandler.ApplyMaxManifestBytes(current.Storage.MaxManifestBytes)
		tmpTTL, trashTTL := current.StorageDurations()
		if err := storageManager.ApplyPolicy(storage.Policy{
			Settings: storage.Settings{
				MaxBytes:      current.Storage.MaxBytes,
				CacheMaxBytes: current.Storage.CacheMaxBytes,
				MinFreeBytes:  current.Storage.MinFreeBytes,
				HighWatermark: current.Storage.HighWatermark,
				LowWatermark:  current.Storage.LowWatermark,
				OnFull:        current.Storage.OnFull,
			},
			TmpTTL:              tmpTTL,
			TrashTTL:            trashTTL,
			DownloadConcurrency: current.Storage.DownloadConcurrency,
		}); err != nil {
			logger.Error("apply storage runtime policy failed", "error", err)
		}
		if err := upstreamManager.SetOutboundProxy(outboundProxyFromRuntime(current.Outbound)); err != nil {
			logger.Error("apply outbound proxy policy failed", "error", err)
		}
	})
	metricsCollector, err := observability.New(metadata, storageManager, upstreamManager, version, startedAt)
	if err != nil {
		return fmt.Errorf("initialize metrics collector: %w", err)
	}
	registryFrontend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if current := runtimeManager.Current(); current != nil && current.Proxy.Disabled {
			registryHandler.ServeHTTP(w, r)
			return
		}
		proxyHandler.ServeHTTP(w, r)
	})
	healthzHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","version":"` + version + `"}`))
	})
	readyzHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	registerRegistryRoutes := func(mux *http.ServeMux) {
		// Distribution plane: Authorization-based registry authentication only.
		mux.Handle("/v2/token", tokenHandler)
		mux.Handle("/v2/", registryFrontend)
		mux.Handle("GET /healthz", healthzHandler)
		mux.Handle("GET /readyz", readyzHandler)
	}
	registerManagementRoutes := func(mux *http.ServeMux, includeOperationalEndpoints bool) {
		// Management plane: the admin API owns its HttpOnly cookie session boundary.
		mux.Handle("/api/v1/", adminAPI)
		mux.Handle("GET /api/v1/healthz", healthzHandler)
		mux.Handle("GET /api/v1/readyz", readyzHandler)
		mux.Handle("/metrics", metricsCollector)
		if includeOperationalEndpoints {
			mux.Handle("GET /healthz", healthzHandler)
			mux.Handle("GET /readyz", readyzHandler)
		}
		mux.Handle("/", web.Handler())
	}
	hstsEnabled := func() bool {
		current := runtimeManager.Current()
		return current != nil && current.Security.HSTS
	}
	wrapPlane := func(name string, resolver *requestinfo.Resolver, next http.Handler) http.Handler {
		planeLogger := logger.With("plane", name)
		handler := metricsCollector.Instrument(next)
		handler = securityHeadersMiddleware(resolver, hstsEnabled, handler)
		handler = requestLogMiddleware(planeLogger, handler)
		return recoverMiddleware(planeLogger, handler)
	}

	type listenerSpec struct {
		name    string
		listen  string
		handler http.Handler
		tls     config.TLSConfig
	}
	var specs []listenerSpec
	if cfg.AdminSeparate() {
		registryMux := http.NewServeMux()
		registerRegistryRoutes(registryMux)
		managementMux := http.NewServeMux()
		registerManagementRoutes(managementMux, true)
		specs = []listenerSpec{
			{name: "registry", listen: cfg.Server.Listen, handler: wrapPlane("registry", registryResolver, registryMux), tls: cfg.Server.TLS},
			{name: "management", listen: cfg.AdminListen(), handler: wrapPlane("management", adminResolver, managementMux), tls: cfg.AdminTLS()},
		}
	} else {
		mux := http.NewServeMux()
		registerRegistryRoutes(mux)
		registerManagementRoutes(mux, false)
		specs = []listenerSpec{
			{name: "shared", listen: cfg.Server.Listen, handler: wrapPlane("shared", registryResolver, mux), tls: cfg.Server.TLS},
		}
	}

	runCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	go cleanupUploads(runCtx, metadata, casStore, logger)
	go cleanupSessions(runCtx, metadata, logger)
	go storageMaintenance(runCtx, storageManager, func() time.Duration {
		if current := runtimeManager.Current(); current != nil {
			return current.AuditRetentionDuration()
		}
		return auditRetention
	}, logger)

	servers := make([]*http.Server, len(specs))
	listeners := make([]net.Listener, len(specs))
	for i, spec := range specs {
		server := &http.Server{
			Addr:              spec.listen,
			Handler:           spec.handler,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       2 * time.Minute,
			MaxHeaderBytes:    1 << 20,
		}
		listener, listenErr := net.Listen("tcp", spec.listen)
		if listenErr != nil {
			for j := 0; j < i; j++ {
				_ = listeners[j].Close()
			}
			return fmt.Errorf("listen on %s plane %s: %w", spec.listen, spec.name, listenErr)
		}
		servers[i] = server
		listeners[i] = listener
	}
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	go func() {
		<-runCtx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		for index, server := range servers {
			if err := server.Shutdown(shutdownCtx); err != nil {
				logger.Error("graceful shutdown failed", "plane", specs[index].name, "error", err)
			}
		}
	}()

	logger.Info("crproxy starting",
		"version", version,
		"plane_mode", cfg.ListenerMode(),
		"registry_listen", cfg.Server.Listen,
		"management_listen", cfg.AdminListen(),
		"registry_url", cfg.RegistryPublicURL(),
		"management_url", cfg.AdminPublicURL(),
		"storage_root", root,
		"trusted_proxies", len(cfg.Server.TrustedProxies),
		"allow_anonymous", runtimeCurrent.RegistryAuth.AllowAnonymous,
		"outbound_proxy_http", runtimeCurrent.Outbound.HTTPProxy != "",
		"outbound_proxy_https", runtimeCurrent.Outbound.HTTPSProxy != "",
		"upstreams", len(cfg.Upstreams),
		"master_key", masterKeyPath,
		"master_key_version", masterKeyring.CurrentVersion(),
	)
	for _, spec := range specs {
		logger.Info("crproxy plane listening", "plane", spec.name, "listen", spec.listen, "tls", spec.tls.Mode)
	}
	errCh := make(chan error, len(specs))
	for index := range specs {
		go func(index int) {
			spec := specs[index]
			server := servers[index]
			listener := listeners[index]
			switch spec.tls.Mode {
			case "":
				errCh <- server.Serve(listener)
			case "file":
				reloader, reloadErr := tlsutil.NewReloader(spec.tls.CertFile, spec.tls.KeyFile, logger.With("plane", spec.name))
				if reloadErr != nil {
					errCh <- fmt.Errorf("%s plane TLS: %w", spec.name, reloadErr)
					return
				}
				go reloader.Run(runCtx, time.Duration(spec.tls.ReloadSeconds)*time.Second)
				errCh <- server.Serve(tls.NewListener(listener, reloader.Config(tlsMinVersion(spec.tls.MinVersion))))
			default:
				errCh <- fmt.Errorf("unsupported TLS mode %q for %s plane", spec.tls.Mode, spec.name)
			}
		}(index)
	}
	var serveErr error
	for range specs {
		serveResult := <-errCh
		if serveResult != nil && !errors.Is(serveResult, http.ErrServerClosed) && serveErr == nil {
			serveErr = serveResult
			cancel()
		}
	}
	return serveErr
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
	endpoint := flags.String("url", "", "readiness endpoint (default: <registry public URL>/readyz)")
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
		target = cfg.RegistryPublicURL() + "/readyz"
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

func securityHeadersMiddleware(resolver *requestinfo.Resolver, hsts func() bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if hsts != nil && hsts() && resolver.Scheme(r) == "https" {
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

func runtimeBaseFromConfig(cfg config.Config, root string) runtimeconfig.Base {
	outbound := runtimeconfig.OutboundProxy{}
	if cfg.OutboundProxy != nil {
		outbound = runtimeconfig.OutboundProxy{
			HTTPProxy:  cfg.OutboundProxy.HTTPProxy,
			HTTPSProxy: cfg.OutboundProxy.HTTPSProxy,
			NoProxy:    cfg.OutboundProxy.NoProxy,
			Username:   cfg.OutboundProxy.Username,
			Password:   cfg.OutboundProxy.Password,
		}
	}
	return runtimeconfig.Base{
		RegistryAuth: runtimeconfig.RegistryAuth{
			Challenge:        cfg.Auth.Registry.Challenge,
			Service:          cfg.Auth.Registry.Service,
			Realm:            cfg.Realm(),
			AllowAnonymous:   cfg.Auth.Registry.AllowAnonymous,
			AnonymousActions: cfg.Auth.Registry.AnonymousActions,
			TokenTTL:         cfg.Auth.Registry.TokenTTL,
		},
		WebSession: runtimeconfig.WebSession{
			SessionTTL:  cfg.Auth.Web.SessionTTL,
			IdleTimeout: cfg.Auth.Web.IdleTimeout,
		},
		Storage: runtimeconfig.Storage{
			MaxBytes:            cfg.Storage.MaxBytes,
			CacheMaxBytes:       cfg.Storage.CacheMaxBytes,
			MinFreeBytes:        cfg.Storage.MinFreeBytes,
			HighWatermark:       cfg.Storage.HighWatermark,
			LowWatermark:        cfg.Storage.LowWatermark,
			OnFull:              cfg.Storage.OnFull,
			MaxBlobBytes:        cfg.Storage.MaxBlobBytes,
			MaxManifestBytes:    cfg.Storage.MaxManifestBytes,
			DownloadConcurrency: cfg.Storage.DownloadConcurrency,
			TmpTTL:              cfg.Storage.TmpTTL,
			TrashTTL:            cfg.Storage.TrashTTL,
		},
		Uploads:  runtimeconfig.Uploads{UploadsTTL: cfg.Storage.UploadTTL},
		Audit:    runtimeconfig.Audit{AuditRetention: cfg.Storage.AuditRetention},
		Proxy:    runtimeconfig.Proxy{Disabled: cfg.Proxy.Disabled},
		Outbound: outbound,
		Security: runtimeconfig.Security{HSTS: cfg.Server.TLS.HSTS},
		Startup: runtimeconfig.Startup{
			Listen:                   cfg.Server.Listen,
			PublicHost:               cfg.Server.PublicHost,
			PublicScheme:             cfg.PublicScheme(),
			RegistryURL:              cfg.RegistryPublicURL(),
			AdminListen:              cfg.AdminListen(),
			AdminPublicHost:          cfg.AdminPublicHost(),
			AdminPublicScheme:        cfg.AdminPublicScheme(),
			AdminURL:                 cfg.AdminPublicURL(),
			PlaneMode:                cfg.ListenerMode(),
			TrustedProxies:           append([]string(nil), cfg.Server.TrustedProxies...),
			TLSMode:                  cfg.Server.TLS.Mode,
			TLSMinVersion:            cfg.Server.TLS.MinVersion,
			TLSReloadSeconds:         cfg.Server.TLS.ReloadSeconds,
			TLSCertificateConfigured: cfg.Server.TLS.CertFile != "",
			TLSKeyConfigured:         cfg.Server.TLS.KeyFile != "",
			StorageRoot:              root,
			ProxyPathPrefix:          cfg.Proxy.PathPrefix,
			BootstrapAdminUsername:   cfg.Auth.BootstrapAdmin.Username,
			JWTSecretConfigured:      true,
			MasterKeyConfigured:      true,
		},
	}
}

func outboundProxyFromRuntime(value runtimeconfig.OutboundProxy) *proxy.OutboundProxy {
	return &proxy.OutboundProxy{
		HTTPProxy:  value.HTTPProxy,
		HTTPSProxy: value.HTTPSProxy,
		NoProxy:    value.NoProxy,
		Username:   value.Username,
		Password:   value.Password,
	}
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

func storageMaintenance(ctx context.Context, manager *storage.Manager, auditRetention func() time.Duration, logger *slog.Logger) {
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
			if err := manager.EnforceRetention(ctx, auditRetention()); err != nil {
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
