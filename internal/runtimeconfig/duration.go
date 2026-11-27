package runtimeconfig

import "time"

func (c Config) TokenTTLDuration() time.Duration {
	value, _ := time.ParseDuration(c.RegistryAuth.TokenTTL)
	return value
}

func (c Config) WebSessionDurations() (time.Duration, time.Duration) {
	sessionTTL, _ := time.ParseDuration(c.WebSession.SessionTTL)
	idleTimeout, _ := time.ParseDuration(c.WebSession.IdleTimeout)
	return sessionTTL, idleTimeout
}

func (c Config) StorageDurations() (time.Duration, time.Duration) {
	tmpTTL, _ := time.ParseDuration(c.Storage.TmpTTL)
	trashTTL, _ := time.ParseDuration(c.Storage.TrashTTL)
	return tmpTTL, trashTTL
}

func (c Config) UploadTTLDuration() time.Duration {
	value, _ := time.ParseDuration(c.Uploads.UploadsTTL)
	return value
}

func (c Config) AuditRetentionDuration() time.Duration {
	value, _ := time.ParseDuration(c.Audit.AuditRetention)
	return value
}
