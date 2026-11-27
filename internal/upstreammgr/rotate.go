package upstreammgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kube-dosh/crproxy/internal/secretbox"
	"github.com/kube-dosh/crproxy/internal/store"
)

type RotationResult struct {
	PreviousVersion int64 `json:"previousVersion"`
	CurrentVersion  int64 `json:"currentVersion"`
	GeneratedKey    bool  `json:"generatedKey"`
	Rewrapped       int   `json:"rewrappedCredentials"`
}

// RotateMasterKey generates a new key when all credentials already use the
// current key, then rewraps every upstream credential under that key. If a
// previous run was interrupted, calling it again resumes with the existing
// current key. Old key versions remain available in the keyring.
func RotateMasterKey(ctx context.Context, metadata *store.Store, keys *secretbox.Keyring) (RotationResult, error) {
	if metadata == nil || keys == nil {
		return RotationResult{}, errors.New("master key rotation requires store and keyring")
	}
	records, err := metadata.ListUpstreams(ctx)
	if err != nil {
		return RotationResult{}, err
	}
	previous := keys.CurrentVersion()
	result := RotationResult{PreviousVersion: previous, CurrentVersion: previous}
	outdated := false
	for _, record := range records {
		if len(record.Credentials) > 0 && record.CredentialsKeyVer != previous {
			outdated = true
			break
		}
	}
	if !outdated {
		next, err := keys.GenerateVersion()
		if err != nil {
			return result, fmt.Errorf("generate master key version: %w", err)
		}
		result.GeneratedKey = true
		result.CurrentVersion = next
	}
	target := keys.CurrentVersion()
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if len(record.Credentials) == 0 {
			if record.CredentialsKeyVer != target {
				if _, err := metadata.SetUpstreamCredentials(ctx, record.ID, nil, target); err != nil {
					return result, fmt.Errorf("update empty credentials key version for %s: %w", record.ID, err)
				}
			}
			continue
		}
		oldSealer, err := keys.Sealer(record.CredentialsKeyVer)
		if err != nil {
			return result, fmt.Errorf("open key version for %s: %w", record.ID, err)
		}
		plaintext, err := oldSealer.Open(record.Credentials, credentialsAAD(record.ID, record.CredentialsVersion))
		if err != nil {
			return result, fmt.Errorf("decrypt credentials for %s: %w", record.ID, err)
		}
		var credentials Credentials
		if err := json.Unmarshal(plaintext, &credentials); err != nil {
			clearBytes(plaintext)
			return result, fmt.Errorf("decode credentials for %s: %w", record.ID, err)
		}
		clearBytes(plaintext)
		newSealer, err := keys.Sealer(target)
		if err != nil {
			return result, err
		}
		newVersion := record.CredentialsVersion + 1
		encoded, err := json.Marshal(credentials)
		if err != nil {
			return result, err
		}
		ciphertext, err := newSealer.Seal(encoded, credentialsAAD(record.ID, newVersion))
		clearBytes(encoded)
		if err != nil {
			return result, fmt.Errorf("encrypt credentials for %s: %w", record.ID, err)
		}
		storedVersion, err := metadata.SetUpstreamCredentials(ctx, record.ID, ciphertext, target)
		if err != nil {
			return result, fmt.Errorf("persist credentials for %s: %w", record.ID, err)
		}
		if storedVersion != newVersion {
			return result, fmt.Errorf("credentials for %s changed concurrently during rotation", record.ID)
		}
		result.Rewrapped++
	}
	return result, nil
}
