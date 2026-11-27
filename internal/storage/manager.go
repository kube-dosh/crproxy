package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"crproxy/internal/cas"
	"crproxy/internal/oci"
	"crproxy/internal/store"
)

var (
	ErrInsufficientStorage = errors.New("insufficient storage")
	ErrInvalidSettings     = errors.New("invalid storage settings")
)

type Settings struct {
	MaxBytes      int64   `json:"maxBytes"`
	CacheMaxBytes int64   `json:"cacheMaxBytes"`
	MinFreeBytes  int64   `json:"minFreeBytes"`
	HighWatermark float64 `json:"highWatermark"`
	LowWatermark  float64 `json:"lowWatermark"`
	OnFull        string  `json:"onFull"`
}

type Config struct {
	Root                string
	Settings            Settings
	TmpTTL              time.Duration
	TrashTTL            time.Duration
	DownloadConcurrency int
}

type Policy struct {
	Settings            Settings
	TmpTTL              time.Duration
	TrashTTL            time.Duration
	DownloadConcurrency int
}

type Manager struct {
	store           *store.Store
	cas             *cas.Store
	root            string
	tmpTTL          time.Duration
	trashTTL        time.Duration
	logger          *slog.Logger
	settingsMu      sync.RWMutex
	settings        Settings
	gcMu            sync.Mutex
	gcStatusMu      sync.RWMutex
	gcStatus        GCStatus
	downloadLimiter *downloadLimiter
}

type DiskInfo struct {
	TotalBytes     int64   `json:"totalBytes"`
	FreeBytes      int64   `json:"freeBytes"`
	AvailableBytes int64   `json:"availableBytes"`
	UsedPercent    float64 `json:"usedPercent"`
}

type Summary struct {
	Settings        Settings           `json:"settings"`
	Usage           store.StorageUsage `json:"usage"`
	Disk            DiskInfo           `json:"disk"`
	State           string             `json:"state"`
	PhysicalRatio   float64            `json:"physicalRatio"`
	CacheRatio      float64            `json:"cacheRatio"`
	TempBytes       int64              `json:"tempBytes"`
	ActiveDownloads int64              `json:"activeDownloads"`
	GC              GCStatus           `json:"gc"`
}

type GCStatus struct {
	Running        bool       `json:"running"`
	LastStartedAt  *time.Time `json:"lastStartedAt,omitempty"`
	LastFinishedAt *time.Time `json:"lastFinishedAt,omitempty"`
	LastError      string     `json:"lastError,omitempty"`
	LastResult     *GCResult  `json:"lastResult,omitempty"`
}

type GCResult struct {
	OrphanContents int   `json:"orphanContents"`
	OrphanBytes    int64 `json:"orphanBytes"`
	EvictedRepos   int   `json:"evictedRepositories"`
	EvictedBytes   int64 `json:"evictedBytes"`
	TrashRemoved   int64 `json:"trashRemoved"`
	TmpRemoved     int64 `json:"tmpRemoved"`
	DurationMS     int64 `json:"durationMs"`
}

func New(metadata *store.Store, casStore *cas.Store, cfg Config, logger *slog.Logger) (*Manager, error) {
	if metadata == nil || casStore == nil {
		return nil, errors.New("storage manager requires store and CAS")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Root == "" {
		cfg.Root = casStore.Root()
	}
	if cfg.TmpTTL <= 0 {
		cfg.TmpTTL = time.Hour
	}
	if cfg.TrashTTL <= 0 {
		cfg.TrashTTL = 24 * time.Hour
	}
	if cfg.DownloadConcurrency <= 0 {
		cfg.DownloadConcurrency = 8
	}
	if err := validateSettings(cfg.Settings); err != nil {
		return nil, err
	}
	manager := &Manager{
		store:           metadata,
		cas:             casStore,
		root:            cfg.Root,
		tmpTTL:          cfg.TmpTTL,
		trashTTL:        cfg.TrashTTL,
		logger:          logger,
		settings:        cfg.Settings,
		downloadLimiter: newDownloadLimiter(cfg.DownloadConcurrency),
	}
	if raw, err := metadata.Setting(context.Background(), "storage.settings"); err == nil {
		var persisted Settings
		if json.Unmarshal([]byte(raw), &persisted) == nil && validateSettings(persisted) == nil {
			manager.settings = persisted
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return manager, nil
}

func validateSettings(settings Settings) error {
	if settings.MaxBytes <= 0 {
		return fmt.Errorf("%w: maxBytes must be positive", ErrInvalidSettings)
	}
	if settings.CacheMaxBytes <= 0 || settings.CacheMaxBytes > settings.MaxBytes {
		return fmt.Errorf("%w: cacheMaxBytes must be positive and no greater than maxBytes", ErrInvalidSettings)
	}
	if settings.MinFreeBytes < 0 {
		return fmt.Errorf("%w: minFreeBytes cannot be negative", ErrInvalidSettings)
	}
	if settings.HighWatermark <= 0 || settings.HighWatermark > 1 {
		return fmt.Errorf("%w: highWatermark must be in (0, 1]", ErrInvalidSettings)
	}
	if settings.LowWatermark <= 0 || settings.LowWatermark >= settings.HighWatermark {
		return fmt.Errorf("%w: lowWatermark must be in (0, highWatermark)", ErrInvalidSettings)
	}
	if settings.OnFull != "bypass" && settings.OnFull != "reject" {
		return fmt.Errorf("%w: onFull must be bypass or reject", ErrInvalidSettings)
	}
	return nil
}

func (m *Manager) Settings() Settings {
	m.settingsMu.RLock()
	defer m.settingsMu.RUnlock()
	return m.settings
}

func (m *Manager) Policy() Policy {
	m.settingsMu.RLock()
	defer m.settingsMu.RUnlock()
	return Policy{Settings: m.settings, TmpTTL: m.tmpTTL, TrashTTL: m.trashTTL, DownloadConcurrency: m.downloadLimiterLimit()}
}

func (m *Manager) downloadLimiterLimit() int {
	m.downloadLimiter.mu.Lock()
	defer m.downloadLimiter.mu.Unlock()
	return m.downloadLimiter.limit
}

func (m *Manager) ApplyPolicy(policy Policy) error {
	if err := validateSettings(policy.Settings); err != nil {
		return err
	}
	if policy.TmpTTL <= 0 {
		return fmt.Errorf("%w: tmp TTL must be positive", ErrInvalidSettings)
	}
	if policy.TrashTTL <= 0 {
		return fmt.Errorf("%w: trash TTL must be positive", ErrInvalidSettings)
	}
	if policy.DownloadConcurrency <= 0 || policy.DownloadConcurrency > 256 {
		return fmt.Errorf("%w: download concurrency must be between 1 and 256", ErrInvalidSettings)
	}
	m.settingsMu.Lock()
	m.settings = policy.Settings
	m.tmpTTL = policy.TmpTTL
	m.trashTTL = policy.TrashTTL
	m.settingsMu.Unlock()
	m.downloadLimiter.setLimit(policy.DownloadConcurrency)
	return nil
}

func (m *Manager) tmpTTLDuration() time.Duration {
	m.settingsMu.RLock()
	defer m.settingsMu.RUnlock()
	return m.tmpTTL
}

func (m *Manager) trashTTLDuration() time.Duration {
	m.settingsMu.RLock()
	defer m.settingsMu.RUnlock()
	return m.trashTTL
}

func (m *Manager) UpdateSettings(ctx context.Context, settings Settings) (Settings, error) {
	if err := validateSettings(settings); err != nil {
		return Settings{}, err
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return Settings{}, err
	}
	if err := m.store.SetSetting(ctx, "storage.settings", string(raw)); err != nil {
		return Settings{}, err
	}
	m.settingsMu.Lock()
	m.settings = settings
	m.settingsMu.Unlock()
	return settings, nil
}

func (m *Manager) Summary(ctx context.Context) (Summary, error) {
	usage, err := m.store.StorageUsage(ctx)
	if err != nil {
		return Summary{}, err
	}
	disk, err := m.diskInfo()
	if err != nil {
		return Summary{}, err
	}
	tempBytes, err := m.cas.TempBytes()
	if err != nil {
		return Summary{}, err
	}
	settings := m.Settings()
	summary := Summary{
		Settings:        settings,
		Usage:           usage,
		Disk:            disk,
		TempBytes:       tempBytes,
		ActiveDownloads: m.downloadLimiter.count(),
		GC:              m.GCStatus(),
	}
	if settings.MaxBytes > 0 {
		summary.PhysicalRatio = float64(usage.PhysicalBytes) / float64(settings.MaxBytes)
	}
	if settings.CacheMaxBytes > 0 {
		summary.CacheRatio = float64(usage.CacheBytes) / float64(settings.CacheMaxBytes)
	}
	summary.State = storageState(settings, usage, disk)
	return summary, nil
}

func (m *Manager) diskInfo() (DiskInfo, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(m.root, &stat); err != nil {
		return DiskInfo{}, err
	}
	blockSize := int64(stat.Bsize)
	total := int64(stat.Blocks) * blockSize
	free := int64(stat.Bfree) * blockSize
	available := int64(stat.Bavail) * blockSize
	used := 0.0
	if total > 0 {
		used = float64(total-available) / float64(total) * 100
	}
	return DiskInfo{TotalBytes: total, FreeBytes: free, AvailableBytes: available, UsedPercent: used}, nil
}

func storageState(settings Settings, usage store.StorageUsage, disk DiskInfo) string {
	if disk.AvailableBytes <= settings.MinFreeBytes {
		return "emergency"
	}
	physicalRatio := ratio(usage.PhysicalBytes, settings.MaxBytes)
	cacheRatio := ratio(usage.CacheBytes, settings.CacheMaxBytes)
	if physicalRatio >= 0.95 || cacheRatio >= 0.95 {
		return "critical"
	}
	if physicalRatio >= settings.HighWatermark || cacheRatio >= settings.HighWatermark {
		return "high"
	}
	return "normal"
}

func ratio(value, limit int64) float64 {
	if limit <= 0 {
		return 0
	}
	return float64(value) / float64(limit)
}

func (m *Manager) CheckLocalWrite(ctx context.Context, required int64) error {
	if required < 0 {
		required = 0
	}
	summary, err := m.Summary(ctx)
	if err != nil {
		return err
	}
	if summary.Usage.PhysicalBytes+required > summary.Settings.MaxBytes {
		return fmt.Errorf("%w: logical capacity exceeded", ErrInsufficientStorage)
	}
	if summary.Disk.AvailableBytes-required < summary.Settings.MinFreeBytes {
		return fmt.Errorf("%w: minimum free disk space would be violated", ErrInsufficientStorage)
	}
	return nil
}

func (m *Manager) CheckLocalWriteForContent(ctx context.Context, required int64, digest string) error {
	var exists int
	err := m.store.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM contents WHERE digest = ?)`, digest).Scan(&exists)
	if err != nil {
		return err
	}
	if exists == 1 {
		return nil
	}
	return m.CheckLocalWrite(ctx, required)
}

type CacheDecision struct {
	Cache    bool
	Bypass   bool
	Reason   string
	HTTPCode int
}

func (m *Manager) CanCache(ctx context.Context, required int64) (CacheDecision, error) {
	summary, err := m.Summary(ctx)
	if err != nil {
		return CacheDecision{}, err
	}
	if required < 0 {
		required = 0
	}
	full := summary.Usage.PhysicalBytes+required > summary.Settings.MaxBytes ||
		summary.Usage.CacheBytes+required > summary.Settings.CacheMaxBytes ||
		summary.Disk.AvailableBytes-required < summary.Settings.MinFreeBytes ||
		summary.State == "critical" || summary.State == "emergency"
	if !full {
		return CacheDecision{Cache: true}, nil
	}
	if summary.Settings.OnFull == "bypass" {
		return CacheDecision{Cache: false, Bypass: true, Reason: "storage watermark reached"}, nil
	}
	return CacheDecision{Cache: false, Reason: "storage watermark reached; cache writes are rejected", HTTPCode: 507}, nil
}

func (m *Manager) AcquireDownload(ctx context.Context) (func(), error) {
	return m.downloadLimiter.acquire(ctx)
}

func (m *Manager) Recover(ctx context.Context) error {
	deleting, err := m.store.ResetDeletingContents(ctx)
	if err != nil {
		return err
	}
	for _, digestValue := range deleting {
		digest, err := oci.ParseDigest(digestValue)
		if err != nil {
			continue
		}
		if _, err := m.store.MarkContentDeleted(ctx, digestValue); err != nil {
			return err
		}
		if err := m.cas.QuarantineBlob(digest); err != nil {
			return err
		}
		if err := m.store.FinishContentDelete(ctx, digestValue); err != nil {
			return err
		}
	}
	if _, err := m.cas.PurgeTrash(time.Now().Add(-m.trashTTLDuration())); err != nil {
		return err
	}
	if _, err := m.cas.PurgeTmp(time.Now().Add(-m.tmpTTLDuration())); err != nil {
		return err
	}
	return nil
}

func (m *Manager) GC(ctx context.Context, manual bool) (result GCResult, err error) {
	m.gcMu.Lock()
	defer m.gcMu.Unlock()
	started := time.Now()
	m.beginGC(started)
	defer func() { m.finishGC(time.Now(), result, err) }()
	summary, err := m.Summary(ctx)
	if err != nil {
		return result, err
	}
	if manual || summary.State != "normal" {
		result.OrphanContents, result.OrphanBytes, err = m.collectOrphans(ctx, 10000)
		if err != nil {
			return result, err
		}
	}
	summary, err = m.Summary(ctx)
	if err != nil {
		return result, err
	}
	if manual || summary.State != "normal" {
		targetPhysical := int64(float64(summary.Settings.MaxBytes) * summary.Settings.LowWatermark)
		targetCache := int64(float64(summary.Settings.CacheMaxBytes) * summary.Settings.LowWatermark)
		repos, err := m.store.CacheEvictionCandidates(ctx, 1000)
		if err != nil {
			return result, err
		}
		for _, repo := range repos {
			if !manual && summary.Usage.PhysicalBytes <= targetPhysical && summary.Usage.CacheBytes <= targetCache && summary.Disk.AvailableBytes > summary.Settings.MinFreeBytes*2 {
				break
			}
			if repo.Bytes <= 0 {
				continue
			}
			before := summary.Usage.PhysicalBytes
			if err := m.store.DeleteRepositoryReferences(ctx, repo.RepositoryID); err != nil {
				return result, err
			}
			orphanCount, orphanBytes, err := m.collectOrphans(ctx, 10000)
			if err != nil {
				return result, err
			}
			result.EvictedRepos++
			result.EvictedBytes += orphanBytes
			result.OrphanContents += orphanCount
			result.OrphanBytes += orphanBytes
			summary, err = m.Summary(ctx)
			if err != nil {
				return result, err
			}
			if summary.Usage.PhysicalBytes >= before && !manual {
				break
			}
		}
	}
	result.TrashRemoved, err = m.cas.PurgeTrash(time.Now().Add(-m.trashTTLDuration()))
	if err != nil {
		return result, err
	}
	result.TmpRemoved, err = m.cas.PurgeTmp(time.Now().Add(-m.tmpTTLDuration()))
	if err != nil {
		return result, err
	}
	result.DurationMS = time.Since(started).Milliseconds()
	return result, nil
}

func (m *Manager) GCStatus() GCStatus {
	m.gcStatusMu.RLock()
	defer m.gcStatusMu.RUnlock()
	status := m.gcStatus
	return status
}

func (m *Manager) beginGC(started time.Time) {
	m.gcStatusMu.Lock()
	defer m.gcStatusMu.Unlock()
	startedAt := started.UTC()
	m.gcStatus.Running = true
	m.gcStatus.LastStartedAt = &startedAt
	m.gcStatus.LastError = ""
}

func (m *Manager) finishGC(finished time.Time, result GCResult, err error) {
	m.gcStatusMu.Lock()
	defer m.gcStatusMu.Unlock()
	finishedAt := finished.UTC()
	m.gcStatus.Running = false
	m.gcStatus.LastFinishedAt = &finishedAt
	if err != nil {
		m.gcStatus.LastError = err.Error()
		return
	}
	copy := result
	m.gcStatus.LastResult = &copy
}

func (m *Manager) collectOrphans(ctx context.Context, limit int) (int, int64, error) {
	orphans, err := m.store.OrphanContents(ctx, limit)
	if err != nil {
		return 0, 0, err
	}
	var count int
	var bytes int64
	for _, content := range orphans {
		digest, err := oci.ParseDigest(content.Digest)
		if err != nil {
			continue
		}
		marked, err := m.store.MarkContentDeleted(ctx, content.Digest)
		if err != nil {
			return count, bytes, err
		}
		if !marked {
			continue
		}
		if err := m.cas.QuarantineBlob(digest); err != nil {
			return count, bytes, err
		}
		if err := m.store.FinishContentDelete(ctx, content.Digest); err != nil {
			return count, bytes, err
		}
		count++
		bytes += content.Size
	}
	return count, bytes, nil
}

func (m *Manager) EvictRepository(ctx context.Context, repositoryID int64) (GCResult, error) {
	repository, err := m.store.RepositoryByID(ctx, repositoryID)
	if err != nil {
		return GCResult{}, err
	}
	if repository.Kind != "cache" {
		return GCResult{}, errors.New("only cache repositories can be evicted")
	}
	if err := m.store.DeleteRepositoryReferences(ctx, repositoryID); err != nil {
		return GCResult{}, err
	}
	count, bytes, err := m.collectOrphans(ctx, 10000)
	if err != nil {
		return GCResult{}, err
	}
	return GCResult{OrphanContents: count, OrphanBytes: bytes, EvictedRepos: 1, EvictedBytes: bytes}, nil
}

func (m *Manager) EvictRepositoriesForDigest(ctx context.Context, digest string) (GCResult, error) {
	repositories, _, err := m.store.ListRepositories(ctx, store.RepositoryFilter{Kind: "cache", Limit: 500})
	if err != nil {
		return GCResult{}, err
	}
	total := GCResult{}
	for _, repository := range repositories {
		contents, _, err := m.store.ListContents(ctx, store.ContentFilter{RepositoryID: repository.ID, Query: digest, Limit: 1})
		if err != nil {
			return total, err
		}
		if len(contents) == 0 || contents[0].Digest != digest {
			continue
		}
		result, err := m.EvictRepository(ctx, repository.ID)
		if err != nil {
			return total, err
		}
		total.EvictedRepos += result.EvictedRepos
		total.EvictedBytes += result.EvictedBytes
		total.OrphanContents += result.OrphanContents
		total.OrphanBytes += result.OrphanBytes
	}
	return total, nil
}

type FsckIssue struct {
	Type     string `json:"type"`
	Digest   string `json:"digest,omitempty"`
	Path     string `json:"path,omitempty"`
	Expected int64  `json:"expectedSize,omitempty"`
	Actual   int64  `json:"actualSize,omitempty"`
	Local    bool   `json:"local"`
	Message  string `json:"message"`
}

type FsckResult struct {
	Mode            string      `json:"mode"`
	CheckedContents int         `json:"checkedContents"`
	CheckedFiles    int         `json:"checkedFiles"`
	Issues          []FsckIssue `json:"issues"`
	OrphanFiles     int         `json:"orphanFiles"`
	CorruptContents int         `json:"corruptContents"`
	TmpRemoved      int64       `json:"tmpRemoved"`
	TrashRemoved    int64       `json:"trashRemoved"`
	DurationMS      int64       `json:"durationMs"`
}

func (m *Manager) Fsck(ctx context.Context, mode string) (FsckResult, error) {
	if mode == "" {
		mode = "quick"
	}
	if mode != "quick" && mode != "full" {
		return FsckResult{}, fmt.Errorf("invalid fsck mode %q", mode)
	}
	started := time.Now()
	result := FsckResult{Mode: mode, Issues: []FsckIssue{}}
	localRefs, err := m.store.ContentsWithLocalRefs(ctx)
	if err != nil {
		return result, err
	}
	contents, err := m.store.AllContents(ctx)
	if err != nil {
		return result, err
	}
	for _, content := range contents {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.CheckedContents++
		digest, err := oci.ParseDigest(content.Digest)
		if err != nil {
			result.Issues = append(result.Issues, FsckIssue{Type: "invalid_digest", Digest: content.Digest, Local: localRefs[content.Digest], Message: "database digest is invalid"})
			result.CorruptContents++
			continue
		}
		file, info, err := m.cas.OpenBlob(digest)
		if err != nil {
			issue := FsckIssue{Type: "missing_file", Digest: content.Digest, Local: localRefs[content.Digest], Expected: content.Size, Message: "CAS file is missing"}
			result.Issues = append(result.Issues, issue)
			result.CorruptContents++
			continue
		}
		if info.Size() != content.Size {
			result.Issues = append(result.Issues, FsckIssue{Type: "size_mismatch", Digest: content.Digest, Local: localRefs[content.Digest], Expected: content.Size, Actual: info.Size(), Message: "CAS file size does not match metadata"})
			result.CorruptContents++
		}
		if mode == "full" {
			actual, _, hashErr := oci.FromReader(digest.Algorithm, file)
			if hashErr != nil || actual != digest {
				result.Issues = append(result.Issues, FsckIssue{Type: "digest_mismatch", Digest: content.Digest, Local: localRefs[content.Digest], Message: "CAS file content does not match digest"})
				result.CorruptContents++
			} else if err := m.store.MarkContentVerified(ctx, content.Digest); err != nil {
				_ = file.Close()
				return result, err
			}
		}
		_ = file.Close()
	}
	known, err := m.store.ContentDigestSet(ctx)
	if err != nil {
		return result, err
	}
	var orphanDigests []oci.Digest
	err = m.cas.WalkBlobs(func(digest oci.Digest, _ os.FileInfo) error {
		result.CheckedFiles++
		if !known[digest.String()] {
			orphanDigests = append(orphanDigests, digest)
			result.OrphanFiles++
			result.Issues = append(result.Issues, FsckIssue{Type: "orphan_file", Digest: digest.String(), Path: filepath.Join("cas", digest.Algorithm), Message: "CAS file has no metadata record"})
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	for _, digest := range orphanDigests {
		if err := m.cas.QuarantineBlob(digest); err != nil {
			return result, err
		}
	}
	result.TmpRemoved, err = m.cas.PurgeTmp(time.Now().Add(-m.tmpTTLDuration()))
	if err != nil {
		return result, err
	}
	result.TrashRemoved, err = m.cas.PurgeTrash(time.Now().Add(-m.trashTTLDuration()))
	if err != nil {
		return result, err
	}
	result.DurationMS = time.Since(started).Milliseconds()
	return result, nil
}

func (m *Manager) EnforceRetention(ctx context.Context, auditRetention time.Duration) error {
	cutoff := time.Now().Add(-auditRetention)
	if _, err := m.store.DeleteAuditEventsBefore(ctx, cutoff); err != nil {
		return err
	}
	if _, err := m.store.DeleteRegistryEventsBefore(ctx, cutoff); err != nil {
		return err
	}
	return nil
}

func (m *Manager) ProtectedDigest(ctx context.Context, digest string) (bool, error) {
	refs, err := m.store.ContentsWithLocalRefs(ctx)
	if err != nil {
		return false, err
	}
	return refs[digest], nil
}

func (m *Manager) Root() string {
	return m.root
}

func NormalizeSettings(settings Settings) Settings {
	settings.OnFull = strings.ToLower(strings.TrimSpace(settings.OnFull))
	return settings
}
