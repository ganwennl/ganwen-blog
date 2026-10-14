package blog

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"
)

// DSN is supplied privately by the CLI environment/runtime.env, never flags or logs.
type DatabaseConfig struct{ Driver, DSN, Path string }

func mysqlConnectionConfig(dsn string) (*mysql.Config, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("GBLOG_DATABASE_DSN is required for MySQL")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || cfg.DBName == "" {
		return nil, errors.New("invalid MySQL database configuration")
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.MultiStatements = false
	cfg.ClientFoundRows = false
	cfg.InterpolateParams = false
	cfg.AllowAllFiles = false
	cfg.AllowCleartextPasswords = false
	cfg.AllowOldPasswords = false
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 30 * time.Second
	cfg.WriteTimeout = 30 * time.Second
	cfg.Logger = log.New(io.Discard, "", 0)
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["time_zone"] = "'+00:00'"
	if err = cfg.Apply(mysql.Charset("utf8mb4", "utf8mb4_bin")); err != nil {
		return nil, errors.New("invalid MySQL charset configuration")
	}
	return cfg, nil
}

func OpenDatabase(cfg DatabaseConfig) (*Store, error) {
	driver := strings.ToLower(strings.TrimSpace(cfg.Driver))
	if driver == "" {
		driver = "sqlite"
	}
	var db *sql.DB
	var err error
	switch driver {
	case "sqlite":
		if cfg.DSN != "" {
			return nil, errors.New("SQLite uses a local path; do not provide a DSN")
		}
		if cfg.Path == "" {
			cfg.Path = filepath.Join("data", "blog.db")
		}
		if err = os.MkdirAll(filepath.Dir(cfg.Path), 0700); err != nil {
			return nil, err
		}
		db, err = sql.Open("sqlite", cfg.Path)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	case "mysql":
		mysqlCfg, e := mysqlConnectionConfig(cfg.DSN)
		if e != nil {
			return nil, e
		}
		connector, e := mysql.NewConnector(mysqlCfg)
		if e != nil {
			return nil, errors.New("invalid MySQL connector configuration")
		}
		db = sql.OpenDB(connector)
		db.SetMaxOpenConns(8)
		db.SetMaxIdleConns(4)
		db.SetConnMaxLifetime(3 * time.Minute)
		db.SetConnMaxIdleTime(time.Minute)
	default:
		return nil, errors.New("database driver must be sqlite or mysql")
	}
	s := &Store{db: db, driver: driver}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		if driver == "mysql" {
			return nil, errors.New("MySQL connection failed; check private database configuration")
		}
		return nil, err
	}
	if driver == "mysql" {
		// Refuse unrelated schemas (including WordPress); never create a database.
		rows, e := db.QueryContext(ctx, "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE()")
		if e != nil {
			db.Close()
			return nil, errors.New("cannot inspect dedicated MySQL database")
		}
		foreign := false
		for rows.Next() {
			var name string
			if rows.Scan(&name) != nil {
				foreign = true
				break
			}
			if !blogDatabaseTable(name) {
				foreign = true
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil || foreign {
			db.Close()
			return nil, errors.New("MySQL database must be dedicated to this blog; unrelated tables found")
		}
	} else {
		rows, e := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type IN ('table','view')")
		if e != nil {
			db.Close()
			return nil, errors.New("cannot inspect dedicated SQLite database")
		}
		foreign := false
		for rows.Next() {
			var name string
			if rows.Scan(&name) != nil {
				foreign = true
				break
			}
			if !blogDatabaseTable(name) && !strings.HasPrefix(name, "sqlite_") {
				foreign = true
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil || foreign {
			db.Close()
			return nil, errors.New("SQLite database must be dedicated to this blog; unrelated tables found")
		}
		for _, statement := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000", "PRAGMA foreign_keys=ON"} {
			if _, err = db.ExecContext(ctx, statement); err != nil {
				db.Close()
				return nil, err
			}
		}
	}
	for _, statement := range s.schemaStatements() {
		if _, err = db.ExecContext(ctx, statement); err != nil {
			db.Close()
			if driver == "mysql" {
				return nil, errors.New("cannot initialize dedicated MySQL blog schema")
			}
			return nil, err
		}
	}
	if err = s.initializeTranslationJobs(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err = s.initializeTranslationJobsDueIndex(ctx); err != nil {
		db.Close()
		return nil, err
	}
	for _, statement := range s.analyticsSchema() {
		if _, err = db.ExecContext(ctx, statement); err != nil {
			db.Close()
			return nil, errors.New("cannot initialize blog analytics schema")
		}
	}
	if driver == "sqlite" {
		_ = os.Chmod(cfg.Path, 0600)
	}
	return s, nil
}

func (s *Store) schemaStatements() []string {
	if s.driver == "mysql" {
		return []string{
			"CREATE TABLE IF NOT EXISTS posts (id BIGINT NOT NULL PRIMARY KEY,kind VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,published_at VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,document LONGTEXT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin",
			"CREATE TABLE IF NOT EXISTS kv (`key` VARCHAR(191) NOT NULL PRIMARY KEY,value LONGTEXT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin",
		}
	}
	return []string{"CREATE TABLE IF NOT EXISTS posts(id INTEGER PRIMARY KEY,kind TEXT NOT NULL,status TEXT NOT NULL,published_at TEXT NOT NULL,document TEXT NOT NULL)", "CREATE TABLE IF NOT EXISTS kv(`key` TEXT PRIMARY KEY,value TEXT NOT NULL)"}
}
func (s *Store) initializeTranslationJobs(ctx context.Context) error {
	languageConstraint := "'" + strings.Join(translationLanguages(), "','") + "'"
	statement := `CREATE TABLE IF NOT EXISTS translation_jobs (id INTEGER PRIMARY KEY AUTOINCREMENT,post_id INTEGER NOT NULL,language TEXT NOT NULL CHECK(language IN (` + languageConstraint + `)),source_hash TEXT NOT NULL,status TEXT NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',next_attempt_at TEXT NOT NULL,baseline_hash TEXT NOT NULL DEFAULT '',UNIQUE(post_id,language,source_hash))`
	if s.driver == "mysql" {
		statement = "CREATE TABLE IF NOT EXISTS translation_jobs (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,post_id BIGINT NOT NULL,language VARCHAR(2) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,source_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,attempts INT NOT NULL DEFAULT 0,last_error VARCHAR(2048) NOT NULL DEFAULT '',next_attempt_at VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,baseline_hash VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',UNIQUE KEY translation_source(post_id,language,source_hash)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin"
	}
	_, err := s.db.ExecContext(ctx, statement)
	if err != nil || s.driver == "mysql" {
		return err
	}
	var existingSQL string
	if err = s.db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='translation_jobs'").Scan(&existingSQL); err != nil {
		return err
	}
	complete := true
	for _, language := range translationLanguages() {
		complete = complete && strings.Contains(existingSQL, "'"+language+"'")
	}
	if complete {
		return nil
	}
	// SQLite cannot alter a CHECK constraint. Rebuild transactionally, preserving
	// every job, its ID and the original AUTOINCREMENT high-water mark.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sequence int64
	if err = tx.QueryRowContext(ctx, "SELECT seq FROM sqlite_sequence WHERE name='translation_jobs'").Scan(&sequence); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	replacement := strings.Replace(statement, "IF NOT EXISTS translation_jobs", "translation_jobs_language_v2", 1)
	if _, err = tx.ExecContext(ctx, replacement); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO translation_jobs_language_v2(id,post_id,language,source_hash,status,attempts,last_error,next_attempt_at,baseline_hash) SELECT id,post_id,language,source_hash,status,attempts,last_error,next_attempt_at,baseline_hash FROM translation_jobs"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DROP TABLE translation_jobs"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "ALTER TABLE translation_jobs_language_v2 RENAME TO translation_jobs"); err != nil {
		return err
	}
	if sequence > 0 {
		var current int64
		sequenceErr := tx.QueryRowContext(ctx, "SELECT seq FROM sqlite_sequence WHERE name='translation_jobs'").Scan(&current)
		if errors.Is(sequenceErr, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, "INSERT INTO sqlite_sequence(name,seq) VALUES('translation_jobs',?)", sequence)
		} else if sequenceErr != nil {
			return sequenceErr
		} else if current < sequence {
			_, err = tx.ExecContext(ctx, "UPDATE sqlite_sequence SET seq=? WHERE name='translation_jobs'", sequence)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) initializeTranslationJobsDueIndex(ctx context.Context) error {
	const name = "translation_jobs_due"
	const columns = "status,next_attempt_at,id"
	if s.driver != "mysql" {
		_, err := s.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS "+name+" ON translation_jobs("+columns+")")
		return err
	}
	// MySQL has no portable CREATE INDEX IF NOT EXISTS. Inspect the exact
	// columns, and only accept its duplicate-name error after checking again.
	exists := func() (bool, error) {
		rows, err := s.db.QueryContext(ctx, "SELECT COLUMN_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='translation_jobs' AND INDEX_NAME=? ORDER BY SEQ_IN_INDEX", name)
		if err != nil {
			return false, err
		}
		defer rows.Close()
		var found []string
		for rows.Next() {
			var column string
			if err = rows.Scan(&column); err != nil {
				return false, err
			}
			found = append(found, column)
		}
		if err = rows.Err(); err != nil {
			return false, err
		}
		if len(found) == 0 {
			return false, nil
		}
		if strings.Join(found, ",") != columns {
			return false, errors.New("translation due index has unexpected columns")
		}
		return true, nil
	}
	if present, err := exists(); err != nil || present {
		return err
	}
	_, err := s.db.ExecContext(ctx, "CREATE INDEX "+name+" ON translation_jobs("+columns+")")
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1061 {
		if present, checkErr := exists(); checkErr != nil {
			return checkErr
		} else if present {
			return nil
		}
	}
	return err
}

func (s *Store) kvUpsertSQL() string {
	if s.driver == "mysql" {
		return "INSERT INTO kv(`key`,value) VALUES(?,?) ON DUPLICATE KEY UPDATE value=VALUES(value)"
	}
	return "INSERT INTO kv(`key`,value) VALUES(?,?) ON CONFLICT(`key`) DO UPDATE SET value=excluded.value"
}
func (s *Store) postUpsertSQL() string {
	base := "INSERT INTO posts(id,kind,status,published_at,document) VALUES(?,?,?,?,?)"
	if s.driver == "mysql" {
		return base + " ON DUPLICATE KEY UPDATE kind=VALUES(kind),status=VALUES(status),published_at=VALUES(published_at),document=VALUES(document)"
	}
	return base + " ON CONFLICT(id) DO UPDATE SET kind=excluded.kind,status=excluded.status,published_at=excluded.published_at,document=excluded.document"
}
func (s *Store) translationQueueSQL(force bool) string {
	base := "INSERT INTO translation_jobs(post_id,language,source_hash,status,next_attempt_at,baseline_hash) VALUES(?,?,?,'queued',?,?)"
	if s.driver == "mysql" {
		if !force {
			return base + " ON DUPLICATE KEY UPDATE id=id"
		}
		// MySQL evaluates assignments left to right; status must be assigned last.
		condition := "status NOT IN ('queued','running','retry')"
		return base + " ON DUPLICATE KEY UPDATE attempts=IF(" + condition + ",0,attempts),last_error=IF(" + condition + ",'',last_error),next_attempt_at=IF(" + condition + ",VALUES(next_attempt_at),next_attempt_at),baseline_hash=IF(" + condition + ",VALUES(baseline_hash),baseline_hash),status=IF(" + condition + ",'queued',status)"
	}
	if force {
		return base + " ON CONFLICT(post_id,language,source_hash) DO UPDATE SET status='queued',attempts=0,last_error='',next_attempt_at=excluded.next_attempt_at,baseline_hash=excluded.baseline_hash WHERE translation_jobs.status NOT IN ('queued','running','retry')"
	}
	return "INSERT OR IGNORE INTO translation_jobs(post_id,language,source_hash,status,next_attempt_at,baseline_hash) VALUES(?,?,?,'queued',?,?)"
}
