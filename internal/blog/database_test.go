package blog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestDatabaseConfigAndMySQLDialect(t *testing.T) {
	cfg, err := mysqlConnectionConfig("user:private-test-value@tcp(127.0.0.1:3306)/ganwen_blog_test_config?parseTime=false&multiStatements=true&charset=latin1&loc=Local")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ParseTime || cfg.Loc != time.UTC || cfg.MultiStatements || cfg.InterpolateParams || cfg.Params["time_zone"] != "'+00:00'" || cfg.Collation != "utf8mb4_bin" {
		t.Fatal("MySQL safety/Unicode/time defaults missing")
	}
	if !strings.Contains(cfg.FormatDSN(), "charset=utf8mb4") {
		t.Fatal("MySQL charset not enforced")
	}
	if _, err = OpenDatabase(DatabaseConfig{Driver: "postgres"}); err == nil {
		t.Fatal("unknown driver accepted")
	}
	if _, err = mysqlConnectionConfig("sensitive-invalid-dsn"); err == nil || strings.Contains(err.Error(), "sensitive-invalid-dsn") {
		t.Fatal("invalid DSN accepted or leaked")
	}
	s := &Store{driver: "mysql"}
	for _, sql := range []string{s.kvUpsertSQL(), s.postUpsertSQL(), s.translationQueueSQL(false), s.translationQueueSQL(true)} {
		if strings.Contains(sql, "ON CONFLICT") || strings.Contains(sql, "INSERT OR IGNORE") {
			t.Fatal("SQLite SQL in MySQL dialect")
		}
	}
	force := s.translationQueueSQL(true)
	if strings.LastIndex(force, "status=IF") < strings.LastIndex(force, "baseline_hash=IF") {
		t.Fatal("MySQL queue status assignment must be last")
	}
}

func databaseFixture(t *testing.T) (*Store, string, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(filepath.Join(dir, "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	if err = s.SaveSettings(ctx, SiteSettings{Title: "甘文😀", BaseURL: "https://www.ganwen.nl", PostsPerPage: 5}); err != nil {
		t.Fatal(err)
	}
	p := Post{ID: 1447, Version: 3, Type: "post", Status: "publish", Title: "中文😀", ContentHTML: `<p>原文 &amp; 图片 <img src="/wp-content/uploads/a.jpg"></p>`, SourceHTML: "<!-- wp:paragraph -->原始源", CanonicalURL: "https://www.ganwen.nl/?p=1447", PublishedAt: "2025-01-30T06:43:26+08:00", ModifiedAt: "2026-09-01T12:34:56+08:00", AuthorID: 1}
	if err = s.saveLocked(ctx, &p); err != nil {
		t.Fatal(err)
	}
	private := p
	private.ID = 2000
	private.Status = "private"
	private.CanonicalURL = "https://www.ganwen.nl/?p=2000"
	if err = s.saveLocked(ctx, &private); err != nil {
		t.Fatal(err)
	}
	translator, err := NewTranslator(s, Config{DataDir: dir, TranslationModel: "test-model", TranslationKey: "test-only-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err = translator.Queue(ctx, p, false); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "database.json")
	if err = s.ExportDatabaseFile(ctx, file, dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "test-only-secret") {
		t.Fatal("API key plaintext leaked in database export")
	}
	return s, dir, file
}

func TestDatabaseMigrationPreservesEverythingAndRejectsOverwrite(t *testing.T) {
	s, sourceDir, file := databaseFixture(t)
	ctx := context.Background()
	targetDir := t.TempDir()
	target, err := OpenStore(filepath.Join(targetDir, "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err = target.ImportDatabaseFile(ctx, file, "", targetDir); err != nil {
		t.Fatal(err)
	}
	want, err := s.DatabaseSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := target.DatabaseSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want.CreatedAt = got.CreatedAt
	if !reflect.DeepEqual(want, got) {
		t.Fatal("database migration changed posts, KV ciphertext or jobs")
	}
	sourceKey, _ := os.ReadFile(filepath.Join(sourceDir, "translation.key"))
	targetKey, _ := os.ReadFile(filepath.Join(targetDir, "translation.key"))
	if !reflect.DeepEqual(sourceKey, targetKey) {
		t.Fatal("translation encryption key changed")
	}
	translator, err := NewTranslator(target, Config{DataDir: targetDir})
	if err != nil {
		t.Fatal(err)
	}
	if translator.cfg.APIKey != "test-only-secret" {
		t.Fatal("migrated encrypted configuration cannot be decrypted")
	}
	if err = target.ImportDatabaseFile(ctx, file, "", targetDir); err == nil {
		t.Fatal("nonempty target overwritten")
	}
	if err = s.ExportDatabaseFile(ctx, file, sourceDir); err == nil {
		t.Fatal("existing backup overwritten")
	}
	if public, err := target.ListPosts(ctx, false); err != nil || len(public) != 1 {
		t.Fatal("private state changed in migration")
	}
	if err = s.ExportDatabaseFile(ctx, filepath.Join(t.TempDir(), "new.json"), t.TempDir()); err == nil {
		t.Fatal("encrypted configuration exported without original key")
	}
}

func TestDatabaseImportKeyMismatchAndAtomicRollback(t *testing.T) {
	_, _, file := databaseFixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	s, err := OpenStore(filepath.Join(dir, "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = os.WriteFile(filepath.Join(dir, "translation.key"), make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.ImportDatabaseFile(ctx, file, "", dir); err == nil {
		t.Fatal("different destination key overwritten")
	}
	posts, _ := s.ListPosts(ctx, true)
	if len(posts) != 0 {
		t.Fatal("failed import wrote posts")
	}
	raw, _ := os.ReadFile(file)
	var backup DatabaseBackup
	if err = json.Unmarshal(raw, &backup); err != nil {
		t.Fatal(err)
	}
	backup.EncryptionKeySHA256 = ""
	if err = s.RestoreDatabase(ctx, backup); err == nil {
		t.Fatal("encrypted config without key fingerprint accepted")
	}
	backup.Values = nil
	backup.Jobs = nil
	backup.Posts = append(backup.Posts, backup.Posts[0])
	if err = s.RestoreDatabase(ctx, backup); err == nil {
		t.Fatal("duplicate post imported")
	}
	posts, _ = s.ListPosts(ctx, true)
	if len(posts) != 0 {
		t.Fatal("invalid backup partially imported")
	}
}

// Requires a newly created, isolated database; never uses the normal runtime DSN.
// The explicit name prefix and opt-in protect accidental invocation against live data.
func TestMySQLIntegrationRoundTrip(t *testing.T) {
	dsn := os.Getenv("GBLOG_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("isolated MySQL DSN not configured")
	}
	if os.Getenv("GBLOG_TEST_MYSQL_ALLOW_WRITE") != "isolated" {
		t.Fatal("MySQL integration requires explicit isolated test opt-in")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(cfg.DBName, "ganwen_blog_test_") {
		t.Fatal("MySQL integration database must use the isolated ganwen_blog_test_ prefix")
	}
	s, err := OpenDatabase(DatabaseConfig{Driver: "mysql", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = databaseEmpty(ctx, tx)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal("isolated test database is not empty; refusing to modify it")
	}
	source, sourceDir, file := databaseFixture(t)
	if _, err = source.RecordAnalytics(ctx, time.Now().UTC(), "mysql_roundtrip_event_0123456789", "mysql_roundtrip_visitor_0123456789", []AnalyticsAggregate{{Kind: "page", Label: "/?p=1447", PostID: 1447}, {Kind: "language", Label: "fr"}}); err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(t.TempDir(), "mysql-analytics-roundtrip.json")
	if err = source.ExportDatabaseFile(ctx, file, sourceDir); err != nil {
		t.Fatal(err)
	}
	mysqlDir := t.TempDir()
	if err = s.ImportDatabaseFile(ctx, file, "", mysqlDir); err != nil {
		t.Fatal(err)
	}
	want, _ := source.DatabaseSnapshot(ctx)
	got, err := s.DatabaseSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want.CreatedAt = got.CreatedAt
	if !reflect.DeepEqual(want, got) {
		t.Fatal("SQLite to MySQL changed the database")
	}
	t.Run("admin sessions", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		tokenHash := adminHash("mysql-integration-session-token")
		originHash := adminHash("https://mysql-integration.example.test")
		credentialHash := adminHash("mysql-integration-credentials")
		session := adminSession{CSRF: strings.Repeat("s", 43), Expires: now.Add(time.Hour)}
		if err := s.createAdminSession(ctx, tokenHash, "", originHash, credentialHash, session); err != nil {
			t.Fatal("MySQL admin session creation failed", err)
		}
		stored, authenticated, err := s.adminSession(ctx, tokenHash, originHash, credentialHash, now)
		if err != nil || !authenticated || stored.CSRF != session.CSRF || !stored.Expires.Equal(session.Expires) {
			t.Fatal("MySQL admin session read failed", err)
		}
		for _, invalid := range []struct{ origin, credential string }{
			{adminHash("other-origin"), credentialHash},
			{originHash, adminHash("other-credentials")},
		} {
			if _, authenticated, err := s.adminSession(ctx, tokenHash, invalid.origin, invalid.credential, now); err != nil || authenticated {
				t.Fatal("MySQL admin session accepted mismatched authentication", err)
			}
		}
		if _, authenticated, err := s.adminSession(ctx, tokenHash, originHash, credentialHash, session.Expires); err != nil || authenticated {
			t.Fatal("MySQL admin session accepted an expired session", err)
		}
		if err := s.revokeAdminSession(ctx, tokenHash); err != nil {
			t.Fatal("MySQL admin session revocation failed", err)
		}
		if _, authenticated, err := s.adminSession(ctx, tokenHash, originHash, credentialHash, now); err != nil || authenticated {
			t.Fatal("MySQL admin session remained authenticated after revocation", err)
		}
	})
	if inserted, err := s.RecordAnalytics(ctx, time.Now().UTC(), "mysql_roundtrip_event_0123456789", "mysql_roundtrip_visitor_0123456789", nil); err != nil || inserted {
		t.Fatal("MySQL restore lost event dedupe", err)
	}
	if inserted, err := s.RecordAnalytics(ctx, time.Now().UTC(), "mysql_new_event_0123456789", "mysql_roundtrip_visitor_0123456789", nil); err != nil || !inserted {
		t.Fatal("MySQL analytics upsert failed", err)
	}
	if stats, err := s.AnalyticsReport(ctx, time.Now().UTC().Truncate(24*time.Hour), time.Now().UTC().Truncate(24*time.Hour)); err != nil || stats.PV != 2 || stats.UVDailySum != 1 {
		t.Fatal("MySQL event/visitor counts incorrect", err)
	}
	if err = s.SetValue(ctx, "utf8mb4-test", "中文😀第一版"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetValue(ctx, "utf8mb4-test", "中文😀第二版"); err != nil {
		t.Fatal(err)
	}
	v, err := s.GetValue(ctx, "utf8mb4-test")
	if err != nil || v != "中文😀第二版" {
		t.Fatal("MySQL KV upsert/Unicode failed")
	}
	p, err := s.GetPost(ctx, 1447)
	if err != nil {
		t.Fatal(err)
	}
	canonical := p.CanonicalURL
	p.Title = "修改后的中文😀"
	if err = s.SavePost(ctx, &p); err != nil || p.CanonicalURL != canonical {
		t.Fatal("MySQL post update failed")
	}
	created := Post{Title: "新文章😀", ContentHTML: "<p>正文</p>"}
	if err = s.NewPost(ctx, &created); err != nil || created.ID != 10000 {
		t.Fatalf("MySQL ID allocation failed: %v", err)
	}
	translator, err := NewTranslator(s, Config{DataDir: mysqlDir})
	if err != nil {
		t.Fatal(err)
	}
	if translator.cfg.APIKey != "test-only-secret" {
		t.Fatal("MySQL encrypted configuration failed")
	}
	if err = translator.Queue(ctx, p, false); err != nil {
		t.Fatal(err)
	}
	jobs, err := translator.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := len(jobs)
	if err = translator.Queue(ctx, p, false); err != nil {
		t.Fatal(err)
	}
	again, _ := translator.Status(ctx)
	if len(again) != count {
		t.Fatal("MySQL queue deduplication failed")
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE translation_jobs SET status='completed',attempts=4,last_error='test retry' WHERE post_id=? AND source_hash=?", p.ID, SourceHash(p)); err != nil {
		t.Fatal(err)
	}
	if err = translator.Queue(ctx, p, true); err != nil {
		t.Fatal(err)
	}
	jobs, _ = translator.Status(ctx)
	for _, j := range jobs {
		if j.SourceHash == SourceHash(p) && (j.Status != "queued" || j.Attempts != 0 || j.LastError != "") {
			t.Fatal("MySQL force queue did not reset completed job")
		}
	}
	claimed, err := translator.claim(ctx)
	if err != nil || claimed.Status != "running" {
		t.Fatal("MySQL job claim failed")
	}
	translator.finish(ctx, claimed, nil)
	backFile := filepath.Join(t.TempDir(), "mysql-export.json")
	if err = s.ExportDatabaseFile(ctx, backFile, mysqlDir); err != nil {
		t.Fatal(err)
	}
	backDir := t.TempDir()
	back, err := OpenStore(filepath.Join(backDir, "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer back.Close()
	if err = back.ImportDatabaseFile(ctx, backFile, "", backDir); err != nil {
		t.Fatal(err)
	}
	want, err = s.DatabaseSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err = back.DatabaseSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want.CreatedAt = got.CreatedAt
	if !reflect.DeepEqual(want, got) {
		t.Fatal("MySQL to SQLite changed the database")
	}
	var charset, zone string
	if err = s.db.QueryRowContext(ctx, "SELECT @@character_set_connection,@@session.time_zone").Scan(&charset, &zone); err != nil || charset != "utf8mb4" || zone != "+00:00" {
		t.Fatal("MySQL connection Unicode/UTC not applied")
	}
}
