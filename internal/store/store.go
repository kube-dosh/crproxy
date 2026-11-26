package store

import (
	"database/sql"
	"errors"
	"time"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrConflict       = errors.New("conflict")
	ErrBlobReferenced = errors.New("blob is referenced by a manifest")
)

type Store struct {
	DB *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{DB: db}
}

func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

func unixNow() int64 {
	return time.Now().UTC().Unix()
}
