package blog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type DatabaseValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
type DatabaseJob struct {
	ID            int64  `json:"id"`
	PostID        int64  `json:"post_id"`
	Language      string `json:"language"`
	SourceHash    string `json:"source_hash"`
	Status        string `json:"status"`
	Attempts      int    `json:"attempts"`
	LastError     string `json:"last_error"`
	NextAttemptAt string `json:"next_attempt_at"`
	BaselineHash  string `json:"baseline_hash"`
}
type DatabaseBackup struct {
	Format              string           `json:"format"`
	Version             int              `json:"version"`
	CreatedAt           string           `json:"created_at"`
	EncryptionKeySHA256 string           `json:"encryption_key_sha256,omitempty"`
	Posts               []Post           `json:"posts"`
	Values              []DatabaseValue  `json:"values"`
	Jobs                []DatabaseJob    `json:"translation_jobs"`
	Analytics           *AnalyticsBackup `json:"analytics,omitempty"`
}

// DatabaseSnapshot uses a consistent transaction and exports ciphertext verbatim.
// Neither the database DSN nor the encryption key is part of the JSON document.
func (s *Store) DatabaseSnapshot(ctx context.Context) (DatabaseBackup, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return DatabaseBackup{}, err
	}
	defer tx.Rollback()
	out := DatabaseBackup{Format: "ganwen-blog-database", Version: 1, CreatedAt: time.Now().UTC().Format(time.RFC3339), Posts: []Post{}, Values: []DatabaseValue{}, Jobs: []DatabaseJob{}}
	rows, err := tx.QueryContext(ctx, "SELECT document FROM posts ORDER BY id")
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var raw string
		var p Post
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			break
		}
		out.Posts = append(out.Posts, p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT `key`,value FROM kv ORDER BY `key`")
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v DatabaseValue
		if err = rows.Scan(&v.Key, &v.Value); err != nil {
			break
		}
		if !strings.HasPrefix(v.Key, adminSessionKeyPrefix) && v.Key != postsRevisionKey {
			out.Values = append(out.Values, v)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT id,post_id,language,source_hash,status,attempts,last_error,next_attempt_at,baseline_hash FROM translation_jobs ORDER BY id")
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var j DatabaseJob
		if err = rows.Scan(&j.ID, &j.PostID, &j.Language, &j.SourceHash, &j.Status, &j.Attempts, &j.LastError, &j.NextAttemptAt, &j.BaselineHash); err != nil {
			break
		}
		out.Jobs = append(out.Jobs, j)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	out.Analytics, err = snapshotAnalytics(ctx, tx)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func databaseEmpty(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"posts", "kv", "translation_jobs", "analytics_aggregates", "analytics_visitors", "analytics_events"} {
		if table == "kv" {
			// Compare actual keys in Go: SQL collations may ignore trailing
			// spaces, and legacy SQLite primary keys may contain NULL.
			rows, err := tx.QueryContext(ctx, "SELECT `key` FROM kv LIMIT 2")
			if err != nil {
				return err
			}
			for rows.Next() {
				var key sql.NullString
				if err = rows.Scan(&key); err != nil {
					break
				}
				if !key.Valid || key.String != postsRevisionKey {
					err = errors.New("destination database must be empty; refusing to overwrite existing content")
					break
				}
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
			if err != nil {
				return err
			}
			continue
		}
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return errors.New("destination database must be empty; refusing to overwrite existing content")
		}
	}
	return nil
}
func validateDatabaseBackup(b DatabaseBackup) error {
	if b.Format != "ganwen-blog-database" || b.Version != 1 {
		return errors.New("unsupported database backup format")
	}
	if b.EncryptionKeySHA256 != "" {
		raw, e := hex.DecodeString(b.EncryptionKeySHA256)
		if e != nil || len(raw) != sha256.Size {
			return errors.New("invalid encryption-key fingerprint")
		}
	}
	ids := map[int64]bool{}
	if err := validateIndependentPagePaths(b.Posts); err != nil {
		return err
	}
	for _, p := range b.Posts {
		if err := validPost(p); err != nil {
			return fmt.Errorf("backup post %d is invalid", p.ID)
		}
		if ids[p.ID] {
			return errors.New("duplicate backup post ID")
		}
		ids[p.ID] = true
	}
	keys := map[string]bool{}
	for _, v := range b.Values {
		if v.Key == "" || len(v.Key) > 191 || keys[v.Key] {
			return errors.New("invalid or duplicate backup key")
		}
		keys[v.Key] = true
		if v.Key == "site" {
			if err := validateContactNoticeSettingsJSON([]byte(v.Value)); err != nil {
				return err
			}
			if err := validatePublicTrafficSettingsJSON([]byte(v.Value)); err != nil {
				return err
			}
			if err := validateSidebarModulesJSON([]byte(v.Value)); err != nil {
				return err
			}
		}
	}
	jobIDs := map[int64]bool{}
	jobs := map[string]bool{}
	for _, j := range b.Jobs {
		hash, e := hex.DecodeString(j.SourceHash)
		if e != nil || len(hash) != sha256.Size || j.ID <= 0 || j.PostID <= 0 || jobIDs[j.ID] || !isTranslationLanguage(j.Language) || j.Attempts < 0 || len(j.LastError) > 2048 || len(j.NextAttemptAt) > 40 {
			return errors.New("invalid backup translation job")
		}
		switch j.Status {
		case "queued", "running", "retry", "completed", "failed", "cancelled":
		default:
			return errors.New("invalid backup translation status")
		}
		key := fmt.Sprintf("%d/%s/%s", j.PostID, j.Language, j.SourceHash)
		if jobs[key] {
			return errors.New("duplicate backup translation source")
		}
		if j.BaselineHash != "" {
			hash, e = hex.DecodeString(j.BaselineHash)
			if e != nil || len(hash) != sha256.Size {
				return errors.New("invalid backup translation baseline")
			}
		}
		jobs[key] = true
		jobIDs[j.ID] = true
	}
	if databaseHasEncryptedConfig(b) && b.EncryptionKeySHA256 == "" {
		return errors.New("encrypted translation configuration requires its separate original key")
	}
	return validateAnalyticsBackup(b)
}
func databaseHasEncryptedConfig(b DatabaseBackup) bool {
	for _, v := range b.Values {
		if v.Key == translationConfigKey {
			var config persistedTranslationConfig
			if json.Unmarshal([]byte(v.Value), &config) == nil && config.EncryptedAPIKey != "" {
				return true
			}
		}
	}
	return false
}

// RestoreDatabase never upserts user data: only an internal revision may exist.
func (s *Store) RestoreDatabase(ctx context.Context, b DatabaseBackup) error {
	if err := validateDatabaseBackup(b); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = databaseEmpty(ctx, tx); err != nil {
		return err
	}
	for _, p := range b.Posts {
		raw, e := json.Marshal(p)
		if e != nil {
			return e
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO posts(id,kind,status,published_at,document) VALUES(?,?,?,?,?)", p.ID, p.Type, p.Status, p.PublishedAt, string(raw)); err != nil {
			return err
		}
	}
	for _, v := range b.Values {
		// Old or hand-built content backups must never reactivate login sessions.
		if strings.HasPrefix(v.Key, adminSessionKeyPrefix) || v.Key == postsRevisionKey {
			continue
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO kv(`key`,value) VALUES(?,?)", v.Key, v.Value); err != nil {
			return err
		}
	}
	for _, j := range b.Jobs {
		if _, err = tx.ExecContext(ctx, "INSERT INTO translation_jobs(id,post_id,language,source_hash,status,attempts,last_error,next_attempt_at,baseline_hash) VALUES(?,?,?,?,?,?,?,?,?)", j.ID, j.PostID, j.Language, j.SourceHash, j.Status, j.Attempts, j.LastError, j.NextAttemptAt, j.BaselineHash); err != nil {
			return err
		}
	}
	if err = restoreAnalytics(ctx, tx, b.Analytics); err != nil {
		return err
	}
	if err = s.markPostsChanged(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func databaseKeyFingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:])
}
func readDatabaseKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("translation key must be a regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("invalid original translation key")
	}
	return raw, nil
}
func writeDatabasePrivateFile(path string, raw []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

func (s *Store) ExportDatabaseFile(ctx context.Context, file, dataDir string) error {
	b, err := s.DatabaseSnapshot(ctx)
	if err != nil {
		return err
	}
	key, err := readDatabaseKey(filepath.Join(dataDir, "translation.key"))
	if os.IsNotExist(err) {
		err = nil
		key = nil
	}
	if err != nil {
		return errors.New("cannot read original translation key")
	}
	if len(key) > 0 {
		b.EncryptionKeySHA256 = databaseKeyFingerprint(key)
	}
	if err = validateDatabaseBackup(b); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	if _, err = os.Lstat(file); err == nil || !os.IsNotExist(err) {
		return errors.New("backup output exists; refusing to overwrite")
	}
	keyFile := file + ".translation.key"
	if len(key) > 0 {
		if err = writeDatabasePrivateFile(keyFile, key); err != nil {
			return errors.New("cannot create separate key backup; refusing to overwrite")
		}
	}
	if err = writeDatabasePrivateFile(file, raw); err != nil {
		if len(key) > 0 {
			_ = os.Remove(keyFile)
		}
		return err
	}
	return nil
}

func (s *Store) ImportDatabaseFile(ctx context.Context, file, keyFile, dataDir string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 512<<20))
	decoder.DisallowUnknownFields()
	var b DatabaseBackup
	if err = decoder.Decode(&b); err != nil {
		return errors.New("invalid database backup JSON")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("extra database backup data")
	}
	if err = validateDatabaseBackup(b); err != nil {
		return err
	}
	// Check before copying a key; RestoreDatabase checks again in its write transaction.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	err = databaseEmpty(ctx, tx)
	_ = tx.Rollback()
	if err != nil {
		return err
	}
	keyPath := filepath.Join(dataDir, "translation.key")
	created := false
	if b.EncryptionKeySHA256 != "" {
		if keyFile == "" {
			keyFile = file + ".translation.key"
		}
		key, e := readDatabaseKey(keyFile)
		if e != nil || databaseKeyFingerprint(key) != b.EncryptionKeySHA256 {
			return errors.New("original separate translation key is missing or mismatched")
		}
		existing, e := readDatabaseKey(keyPath)
		if e == nil {
			if databaseKeyFingerprint(existing) != b.EncryptionKeySHA256 {
				return errors.New("destination translation key differs; refusing to overwrite")
			}
		} else if os.IsNotExist(e) {
			if err = os.MkdirAll(dataDir, 0700); err != nil {
				return err
			}
			if err = writeDatabasePrivateFile(keyPath, key); err != nil {
				return errors.New("cannot preserve original translation key")
			}
			created = true
		} else {
			return errors.New("cannot inspect destination translation key")
		}
	}
	if err = s.RestoreDatabase(ctx, b); err != nil {
		if created {
			_ = os.Remove(keyPath)
		}
		return err
	}
	return nil
}

func (s *Store) DatabaseDriver() string {
	if strings.TrimSpace(s.driver) == "" {
		return "sqlite"
	}
	return s.driver
}
