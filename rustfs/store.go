package main

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store manages access keys and per-bucket permissions in SQLite.
type Store struct {
	db *sql.DB
}

// Key represents an access key and its metadata.
type Key struct {
	AccessKeyID string
	SecretKey   string
	Description string
	CreatedAt   time.Time
	ExpiresAt   *time.Time
	Active      bool
}

// BucketPermission represents what a key is allowed to do in a specific bucket.
type BucketPermission struct {
	AccessKeyID string
	Bucket      string
	AllowRead   bool
	AllowWrite  bool
	AllowList   bool
}

func NewStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// Single-writer; serialise writes to avoid SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS keys (
			access_key_id TEXT PRIMARY KEY,
			secret_key    TEXT NOT NULL,
			description   TEXT NOT NULL DEFAULT '',
			created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			expires_at    DATETIME,
			active        BOOLEAN NOT NULL DEFAULT 1
		);
		CREATE TABLE IF NOT EXISTS bucket_permissions (
			access_key_id TEXT    NOT NULL REFERENCES keys(access_key_id) ON DELETE CASCADE,
			bucket        TEXT    NOT NULL,
			allow_read    BOOLEAN NOT NULL DEFAULT 0,
			allow_write   BOOLEAN NOT NULL DEFAULT 0,
			allow_list    BOOLEAN NOT NULL DEFAULT 0,
			PRIMARY KEY (access_key_id, bucket)
		);
		CREATE INDEX IF NOT EXISTS idx_perms_key ON bucket_permissions(access_key_id);
	`)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) CreateKey(k Key) error {
	_, err := s.db.Exec(
		`INSERT INTO keys (access_key_id, secret_key, description, expires_at, active)
		 VALUES (?, ?, ?, ?, 1)`,
		k.AccessKeyID, k.SecretKey, k.Description, k.ExpiresAt,
	)
	return err
}

func (s *Store) GetKey(accessKeyID string) (*Key, error) {
	k := &Key{}
	err := s.db.QueryRow(
		`SELECT access_key_id, secret_key, description, created_at, expires_at, active
		 FROM keys WHERE access_key_id = ?`,
		accessKeyID,
	).Scan(&k.AccessKeyID, &k.SecretKey, &k.Description, &k.CreatedAt, &k.ExpiresAt, &k.Active)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return k, err
}

func (s *Store) DeleteKey(accessKeyID string) error {
	_, err := s.db.Exec(`DELETE FROM keys WHERE access_key_id = ?`, accessKeyID)
	return err
}

func (s *Store) ListKeys() ([]Key, error) {
	rows, err := s.db.Query(
		`SELECT access_key_id, secret_key, description, created_at, expires_at, active
		 FROM keys ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []Key
	for rows.Next() {
		var k Key
		if err := rows.Scan(&k.AccessKeyID, &k.SecretKey, &k.Description,
			&k.CreatedAt, &k.ExpiresAt, &k.Active); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *Store) SetBucketPermission(p BucketPermission) error {
	_, err := s.db.Exec(
		`INSERT INTO bucket_permissions (access_key_id, bucket, allow_read, allow_write, allow_list)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(access_key_id, bucket) DO UPDATE SET
		   allow_read  = excluded.allow_read,
		   allow_write = excluded.allow_write,
		   allow_list  = excluded.allow_list`,
		p.AccessKeyID, p.Bucket, p.AllowRead, p.AllowWrite, p.AllowList,
	)
	return err
}

func (s *Store) GetBucketPermission(accessKeyID, bucket string) (*BucketPermission, error) {
	p := &BucketPermission{}
	err := s.db.QueryRow(
		`SELECT access_key_id, bucket, allow_read, allow_write, allow_list
		 FROM bucket_permissions WHERE access_key_id = ? AND bucket = ?`,
		accessKeyID, bucket,
	).Scan(&p.AccessKeyID, &p.Bucket, &p.AllowRead, &p.AllowWrite, &p.AllowList)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return p, err
}

func (s *Store) DeleteBucketPermission(accessKeyID, bucket string) error {
	_, err := s.db.Exec(
		`DELETE FROM bucket_permissions WHERE access_key_id = ? AND bucket = ?`,
		accessKeyID, bucket,
	)
	return err
}

func (s *Store) ListBucketPermissions(accessKeyID string) ([]BucketPermission, error) {
	rows, err := s.db.Query(
		`SELECT access_key_id, bucket, allow_read, allow_write, allow_list
		 FROM bucket_permissions WHERE access_key_id = ? ORDER BY bucket`,
		accessKeyID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var perms []BucketPermission
	for rows.Next() {
		var p BucketPermission
		if err := rows.Scan(&p.AccessKeyID, &p.Bucket,
			&p.AllowRead, &p.AllowWrite, &p.AllowList); err != nil {
			return nil, err
		}
		perms = append(perms, p)
	}
	return perms, rows.Err()
}

// CheckAccess returns true if accessKeyID is valid (active, not expired) and
// has all of the required permissions on bucket.
func (s *Store) CheckAccess(accessKeyID, bucket string, needRead, needWrite, needList bool) (bool, error) {
	k, err := s.GetKey(accessKeyID)
	if err != nil {
		return false, err
	}
	if k == nil || !k.Active {
		return false, nil
	}
	if k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt) {
		return false, nil
	}

	p, err := s.GetBucketPermission(accessKeyID, bucket)
	if err != nil {
		return false, err
	}
	if p == nil {
		return false, nil
	}
	if needRead && !p.AllowRead {
		return false, nil
	}
	if needWrite && !p.AllowWrite {
		return false, nil
	}
	if needList && !p.AllowList {
		return false, nil
	}
	return true, nil
}
