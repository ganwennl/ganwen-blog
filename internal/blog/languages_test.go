package blog

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSQLiteLegacyQueueConstraintUpgradePreservesIDsAndJobs(t *testing.T) {
	for _, keepOldJob := range []bool{true, false} {
		for _, oldLanguages := range []string{"'en','nl'", "'en','nl','es'"} {
			t.Run(map[bool]string{true: "existing-job", false: "empty-high-water"}[keepOldJob]+"/"+oldLanguages, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "old.db")
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec(`CREATE TABLE translation_jobs(id INTEGER PRIMARY KEY AUTOINCREMENT,post_id INTEGER NOT NULL,language TEXT NOT NULL CHECK(language IN (` + oldLanguages + `)),source_hash TEXT NOT NULL,status TEXT NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',next_attempt_at TEXT NOT NULL,baseline_hash TEXT NOT NULL DEFAULT '',UNIQUE(post_id,language,source_hash))`)
				if err != nil {
					t.Fatal(err)
				}
				hash := strings.Repeat("a", 64)
				_, err = db.Exec("INSERT INTO translation_jobs(id,post_id,language,source_hash,status,attempts,last_error,next_attempt_at,baseline_hash) VALUES(10,1447,'nl',?,'retry',3,'retry note','2026-10-01T00:00:00Z',?)", hash, hash)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec("INSERT INTO translation_jobs(id,post_id,language,source_hash,status,next_attempt_at) VALUES(100,2,'en',?,'completed','2026-10-01T00:00:00Z')", hash)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec("DELETE FROM translation_jobs WHERE id=100"); err != nil {
					t.Fatal(err)
				}
				if !keepOldJob {
					if _, err = db.Exec("DELETE FROM translation_jobs"); err != nil {
						t.Fatal(err)
					}
				}
				db.Close()
				s, err := OpenStore(path)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				ctx := context.Background()
				if keepOldJob {
					var j DatabaseJob
					if err = s.db.QueryRowContext(ctx, "SELECT id,post_id,language,status,attempts,last_error,baseline_hash FROM translation_jobs WHERE id=10").Scan(&j.ID, &j.PostID, &j.Language, &j.Status, &j.Attempts, &j.LastError, &j.BaselineHash); err != nil || j.Language != "nl" || j.Status != "retry" || j.Attempts != 3 || j.LastError != "retry note" || j.BaselineHash != hash {
						t.Fatal("legacy queue job changed in constraint upgrade")
					}
				}
				for index, language := range translationLanguages() {
					result, err := s.db.ExecContext(ctx, s.translationQueueSQL(false), 1, language, hash, "2026-10-01T00:00:00Z", "")
					if err != nil {
						t.Fatal(err)
					}
					id, err := result.LastInsertId()
					if err != nil || id != int64(101+index) {
						t.Fatalf("queue AUTOINCREMENT changed: %d %v", id, err)
					}
				}
				if _, err = s.db.ExecContext(ctx, s.translationQueueSQL(true), 1, "de", hash, "2026-10-01T00:00:00Z", ""); err == nil {
					t.Fatal("queue accepted unsupported translation language")
				}
				if err = s.initializeTranslationJobs(ctx); err != nil {
					t.Fatal("constraint migration is not idempotent")
				}
			})
		}
	}
}

func TestBlogLanguageOrderAndMetadata(t *testing.T) {
	want := []string{"zh", "nl", "en", "hi", "es", "ar", "fr", "bn", "pt", "id", "ur"}
	if !reflect.DeepEqual(publicLanguages(), want) || !reflect.DeepEqual(translationLanguages(), want[1:]) {
		t.Fatal("public and translation language ordering changed")
	}
	for _, language := range blogLanguages[1:] {
		if !isTranslationLanguage(language.code) || translationTargetName(language.code) != language.targetName || nativeLanguageName(language.code) != language.nativeName {
			t.Errorf("language metadata incomplete: %s", language.code)
		}
	}
	for _, unsupported := range []string{"de", "zh", "EN", "es-MX", ""} {
		if isTranslationLanguage(unsupported) {
			t.Errorf("accepted unsupported translation language %q", unsupported)
		}
	}
}

func TestAdminEditorSupportsEveryLanguageInPublicOrder(t *testing.T) {
	markup := string(adminUI)
	last := -1
	for _, language := range publicLanguages() {
		position := strings.Index(markup, `data-lang="`+language+`"`)
		if position <= last {
			t.Fatalf("admin editor omitted or reordered language %s", language)
		}
		last = position
		if !strings.Contains(markup, "['"+language+"','"+nativeLanguageName(language)+"',") {
			t.Fatalf("admin translation metadata omitted language %s", language)
		}
	}
	if !strings.Contains(markup, `id="translationExecutionMode"`) || !strings.Contains(markup, `value="paused"`) {
		t.Fatal("admin editor cannot explicitly pause external translation")
	}
}
