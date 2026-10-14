package blog

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTranslationDueIndexSurvivesLanguageMigrationAndReopen(t *testing.T) {
	file := filepath.Join(t.TempDir(), "blog.db")
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `CREATE TABLE translation_jobs(id INTEGER PRIMARY KEY AUTOINCREMENT,post_id INTEGER NOT NULL,language TEXT NOT NULL CHECK(language IN ('en','nl')),source_hash TEXT NOT NULL,status TEXT NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',next_attempt_at TEXT NOT NULL,baseline_hash TEXT NOT NULL DEFAULT '',UNIQUE(post_id,language,source_hash))`
	if _, err = db.Exec(legacy); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE INDEX translation_jobs_due ON translation_jobs(status,next_attempt_at,id)"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	for opening := 0; opening < 2; opening++ {
		s, err := OpenStore(file)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := s.db.Query("PRAGMA index_info(translation_jobs_due)")
		if err != nil {
			s.Close()
			t.Fatal(err)
		}
		var columns []string
		for rows.Next() {
			var seq, cid int
			var name string
			if err = rows.Scan(&seq, &cid, &name); err != nil {
				rows.Close()
				s.Close()
				t.Fatal(err)
			}
			columns = append(columns, name)
		}
		err = rows.Err()
		rows.Close()
		if err != nil || !reflect.DeepEqual(columns, []string{"status", "next_attempt_at", "id"}) {
			s.Close()
			t.Fatal("missing due index after migration/reopen", columns, err)
		}
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTranslationDueQueryUsesIndexWithCompletedHistory(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, "INSERT INTO translation_jobs(post_id,language,source_hash,status,next_attempt_at) VALUES(?,'en',?,'completed','2026-01-01')")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20000; i++ {
		if _, err = statement.ExecContext(ctx, i+1, fmt.Sprintf("%064x", i)); err != nil {
			statement.Close()
			t.Fatal(err)
		}
	}
	statement.Close()
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN SELECT id,post_id,language,source_hash,attempts,last_error,next_attempt_at,baseline_hash FROM translation_jobs WHERE status IN ('queued','retry') AND next_attempt_at<=? ORDER BY id LIMIT 1", "2026-10-09")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	usedIndex := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Log(detail)
		usedIndex = usedIndex || strings.Contains(detail, "translation_jobs_due")
		if strings.Contains(detail, "SCAN translation_jobs") {
			t.Fatal("idle poll scanned completed history", detail)
		}
	}
	if err = rows.Err(); err != nil || !usedIndex {
		t.Fatal("due query did not use due index", err)
	}
}
