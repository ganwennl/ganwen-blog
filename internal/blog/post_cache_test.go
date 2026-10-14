package blog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func cachedTestPost(id int64) Post {
	return Post{ID: id, Version: 1, Type: "post", Status: "publish", Title: "original", ContentHTML: "<p>body</p>", SourceHTML: "source", ExcerptHTML: "excerpt", PublishedAt: "2026-10-09", Categories: []Term{{ID: 1, Name: "category"}}, Tags: []Term{{ID: 2, Name: "tag"}}, Translations: map[string]Translation{"en": {Title: "translation", ContentHTML: "translated body", ExcerptHTML: "translated excerpt", Status: "review"}}}
}

func TestPostCacheCopiesAndSummaries(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(filepath.Join(t.TempDir(), "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := cachedTestPost(1)
	if err = s.saveLocked(ctx, &p); err != nil {
		t.Fatal(err)
	}
	attachment := p
	attachment.ID, attachment.Type = 2, "attachment"
	if err = s.saveLocked(ctx, &attachment); err != nil {
		t.Fatal(err)
	}
	posts, err := s.ListPosts(ctx, false)
	if err != nil || len(posts) != 2 {
		t.Fatal(posts, err)
	}
	for i := range posts {
		posts[i].Title = "caller mutation"
		posts[i].Categories[0].Name = "caller category"
		posts[i].Tags[0].Name = "caller tag"
		tr := posts[i].Translations["en"]
		tr.Title, tr.SEO.Title = "caller translation", "caller SEO"
		posts[i].Translations["en"] = tr
		posts[i].Translations["nl"] = Translation{Title: "caller new language"}
	}
	got, err := s.GetPost(ctx, 1)
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatal("caller changed database", got, err)
	}
	public, err := s.ListPosts(ctx, false)
	if err != nil || !reflect.DeepEqual(public[1], p) {
		t.Fatal("caller polluted cached snapshot", public, err)
	}
	summaries, err := s.ListPostSummaries(ctx)
	if err != nil || len(summaries) != 1 {
		t.Fatal(summaries, err)
	}
	want := p
	want.ContentHTML, want.SourceHTML, want.ExcerptHTML = "", "", ""
	want.Translations = map[string]Translation{"en": p.Translations["en"]}
	tr := want.Translations["en"]
	tr.ContentHTML, tr.ExcerptHTML = "", ""
	want.Translations["en"] = tr
	if !reflect.DeepEqual(summaries[0], want) {
		t.Fatal("summary changed API fields or leaked body", summaries[0])
	}
	summaries[0].Categories[0].Name = "summary mutation"
	delete(summaries[0].Translations, "en")
	again, err := s.ListAdminPosts(ctx)
	if err != nil || !reflect.DeepEqual(again[0], want) {
		t.Fatal("summary polluted cached snapshot", again, err)
	}
	hashes, err := s.PostSourceHashes(ctx, []int64{1, 999})
	if err != nil || len(hashes) != 1 || hashes[1] != SourceHash(p) {
		t.Fatal(hashes, err)
	}
	hashes[1] = "mutated"
	hashes, err = s.PostSourceHashes(ctx, []int64{1})
	if err != nil || hashes[1] != SourceHash(p) {
		t.Fatal("hash map polluted snapshot", hashes, err)
	}
}

func TestPostCacheInvalidationAcrossStoresAndPrivateWithdrawal(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "blog.db")
	reader, err := OpenStore(file)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer, err := OpenStore(file)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	p := cachedTestPost(1)
	if err = writer.saveLocked(ctx, &p); err != nil {
		t.Fatal(err)
	}
	if _, err = reader.ListPosts(ctx, false); err != nil {
		t.Fatal(err)
	}
	before, err := reader.GetValue(ctx, postsRevisionKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.UpdatePost(ctx, 1, func(p *Post) error {
		p.Status, p.ContentHTML = "private", "changed private body"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	after, err := reader.GetValue(ctx, postsRevisionKey)
	if err != nil || after == before {
		t.Fatal("write did not replace persistent token", before, after, err)
	}
	public, err := reader.ListPosts(ctx, false)
	if err != nil || len(public) != 0 {
		t.Fatal("withdrawn private article remained public", public, err)
	}
	all, err := reader.ListPosts(ctx, true)
	if err != nil || len(all) != 1 || all[0].ContentHTML != "changed private body" {
		t.Fatal(all, err)
	}
	if err = writer.UpdatePost(ctx, 1, func(p *Post) error {
		p.Translations["en"] = Translation{Title: "new translation", Status: "review"}
		p.Status = "publish"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	public, err = reader.ListPosts(ctx, false)
	if err != nil || len(public) != 1 || public[0].Translations["en"].Title != "new translation" {
		t.Fatal("translation write did not invalidate", public, err)
	}
	created := cachedTestPost(99)
	if err = writer.NewPost(ctx, &created); err != nil {
		t.Fatal(err)
	}
	public, err = reader.ListPosts(ctx, false)
	if err != nil || len(public) != 2 {
		t.Fatal("new article did not invalidate cached list", public, err)
	}
	created.Status = "private"
	if err = writer.SavePost(ctx, &created); err != nil {
		t.Fatal(err)
	}
	public, err = reader.ListPosts(ctx, false)
	if err != nil || len(public) != 1 || public[0].ID != 1 {
		t.Fatal("SavePost did not revoke new article", public, err)
	}
}

func TestPostCacheLegacyImportAndRestoreInvalidation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := OpenStore(filepath.Join(dir, "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.GetValue(ctx, postsRevisionKey); err != ErrNotFound {
		t.Fatal("opening empty database changed its content", err)
	}
	empty, err := s.ListPosts(ctx, false)
	if err != nil || len(empty) != 0 {
		t.Fatal(empty, err)
	}
	p := cachedTestPost(1)
	raw, err := json.Marshal(Export{Settings: SiteSettings{BaseURL: "https://www.ganwen.nl", PostsPerPage: 10}, Posts: []Post{p}})
	if err != nil {
		t.Fatal(err)
	}
	export := filepath.Join(dir, "import.json")
	if err = os.WriteFile(export, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ImportFile(ctx, export); err != nil {
		t.Fatal(err)
	}
	posts, err := s.ListPosts(ctx, false)
	if err != nil || len(posts) != 1 {
		t.Fatal("import did not invalidate cached empty list", posts, err)
	}
	backup, err := s.DatabaseSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range backup.Values {
		if value.Key == postsRevisionKey {
			t.Fatal("internal revision leaked into portable backup")
		}
	}
	restored, err := OpenStore(filepath.Join(dir, "restored.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err = restored.ListPosts(ctx, false); err != nil {
		t.Fatal(err)
	}
	// A hand-built/old backup cannot revive a revision from another database.
	oldToken, _ := s.GetValue(ctx, postsRevisionKey)
	backup.Values = append(backup.Values, DatabaseValue{Key: postsRevisionKey, Value: oldToken})
	if err = restored.RestoreDatabase(ctx, backup); err != nil {
		t.Fatal(err)
	}
	newToken, err := restored.GetValue(ctx, postsRevisionKey)
	if err != nil || newToken == oldToken || newToken == "" {
		t.Fatal("restore reused cache token", newToken, err)
	}
	posts, err = restored.ListPosts(ctx, false)
	if err != nil || len(posts) != 1 || !reflect.DeepEqual(posts[0], p) {
		t.Fatal("restore did not invalidate cached empty list", posts, err)
	}
}

func TestPostCacheConcurrentSnapshotsAreConsistent(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "blog.db")
	reader, err := OpenStore(file)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer, err := OpenStore(file)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	write := func(revision string) error {
		p := cachedTestPost(1)
		p.Title = revision
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		tx, err := writer.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, writer.postUpsertSQL(), p.ID, p.Type, p.Status, p.PublishedAt, string(raw)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, writer.kvUpsertSQL(), postsRevisionKey, revision); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err = write("initial"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errors := make(chan error, 5)
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				snapshot, err := reader.postSnapshot(ctx)
				if err != nil {
					errors <- err
					return
				}
				if len(snapshot.posts) != 1 || snapshot.posts[0].Title != snapshot.revision {
					errors <- fmt.Errorf("mixed revision/documents: %q %#v", snapshot.revision, snapshot.posts)
					return
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		if err = write(fmt.Sprintf("revision-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
}

func TestPostCacheDatabaseErrorsFailClosed(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	p := cachedTestPost(1)
	ctx := context.Background()
	if err = s.saveLocked(ctx, &p); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ListPosts(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if posts, err := s.ListPosts(ctx, false); err == nil || posts != nil {
		t.Fatal("database failure served a cached public article", posts, err)
	}
}

func TestPostCacheRevisionFailureRollsBackArticle(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	p := cachedTestPost(1)
	if err = s.saveLocked(ctx, &p); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.postSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `CREATE TRIGGER reject_revision BEFORE UPDATE ON kv WHEN OLD.key='posts_revision_v1' BEGIN SELECT RAISE(ABORT,'test revision failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdatePost(ctx, 1, func(p *Post) error {
		p.Status, p.Title = "private", "must roll back"
		return nil
	}); err == nil {
		t.Fatal("revision failure accepted article write")
	}
	stored, err := s.GetPost(ctx, 1)
	if err != nil || !reflect.DeepEqual(stored, p) {
		t.Fatal("article committed without its revision", stored, err)
	}
	after, err := s.postSnapshot(ctx)
	if err != nil || after != snapshot {
		t.Fatal("failed transaction changed cached revision", err)
	}
}

func TestPostCacheSettingsDoNotInvalidateAndStatusMismatchFailsClosed(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, status := range []string{"column_private", "document_private"} {
		p := cachedTestPost(int64(len(status)))
		if status == "document_private" {
			p.Status = "private"
		}
		if err = s.saveLocked(ctx, &p); err != nil {
			t.Fatal(err)
		}
		columnStatus := "publish"
		if status == "column_private" {
			columnStatus = "private"
		}
		if _, err = s.db.ExecContext(ctx, "UPDATE posts SET status=? WHERE id=?", columnStatus, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := s.postSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	public, err := s.ListPosts(ctx, false)
	if err != nil || len(public) != 0 {
		t.Fatal("mismatched private state became public", public, err)
	}
	if err = s.SaveSettings(ctx, SiteSettings{Title: "changed settings", BaseURL: "https://www.ganwen.nl", PostsPerPage: 20}); err != nil {
		t.Fatal(err)
	}
	after, err := s.postSnapshot(ctx)
	if err != nil || after != snapshot {
		t.Fatal("settings invalidated unchanged articles", err)
	}
}

func TestWarmPostCacheDoesNotReturnSuccessAfterCanceledLockWait(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := cachedTestPost(1)
	if err = s.saveLocked(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ListPosts(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	s.postsMu.Lock()
	locked := true
	defer func() {
		if locked {
			s.postsMu.Unlock()
		}
	}()
	reserved, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer reserved.Close()
	initial := s.db.Stats()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		posts, err := s.ListPosts(ctx, false)
		if err != nil && posts != nil {
			done <- errors.New("canceled read returned article data")
			return
		}
		done <- err
	}()
	wait := func(condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !condition() {
			if time.Now().After(deadline) {
				t.Fatalf("reader did not reach controlled cache lock wait: initial=%+v current=%+v", initial, s.db.Stats())
			}
			time.Sleep(time.Millisecond)
		}
	}
	wait(func() bool { return s.db.Stats().WaitCount > initial.WaitCount })
	if err = reserved.Close(); err != nil {
		t.Fatal(err)
	}
	// The queued query receives the reserved connection directly on Close;
	// InUse==0 proves its result closed before cancellation and cache lock release.
	// WaitDuration can remain zero on platforms with a coarse monotonic clock.
	wait(func() bool {
		return s.db.Stats().InUse == 0
	})
	cancel()
	select {
	case err := <-done:
		t.Fatalf("cache read escaped the controlled lock: %v", err)
	default:
	}
	s.postsMu.Unlock()
	locked = false
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled warm cache read returned %v", err)
	}
}

func TestEmptyRestoreIgnoresOnlyReplaceableRevision(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(filepath.Join(t.TempDir(), "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	empty, err := s.DatabaseSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RestoreDatabase(ctx, empty); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetValue(ctx, postsRevisionKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ListPosts(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err = s.RestoreDatabase(ctx, empty); err != nil {
		t.Fatalf("internal revision made a logically empty database nonempty: %v", err)
	}
	after, err := s.GetValue(ctx, postsRevisionKey)
	if err != nil || after == before {
		t.Fatal("repeat empty restore reused its revision", err)
	}
	posts, err := s.ListPosts(ctx, false)
	if err != nil || len(posts) != 0 {
		t.Fatal("empty restore changed article contents", err)
	}
	if err = s.SetValue(ctx, "unrelated_setting", "preserve-me"); err != nil {
		t.Fatal(err)
	}
	if err = s.RestoreDatabase(ctx, empty); err == nil {
		t.Fatal("revision exclusion allowed existing user settings to be overwritten")
	}
	value, err := s.GetValue(ctx, "unrelated_setting")
	if err != nil || value != "preserve-me" {
		t.Fatal("restore rejection lost existing settings", err)
	}
	if _, err = s.db.ExecContext(ctx, "DELETE FROM kv WHERE `key`='unrelated_setting'"); err != nil {
		t.Fatal(err)
	}
	// SQLite permits NULL in this legacy TEXT PRIMARY KEY. It is unrelated
	// existing data and must not disappear through SQL's three-valued filtering.
	if _, err = s.db.ExecContext(ctx, "INSERT INTO kv(`key`,value) VALUES(NULL,'preserve-null-key')"); err != nil {
		t.Fatal(err)
	}
	if err = s.RestoreDatabase(ctx, empty); err == nil {
		t.Fatal("revision exclusion ignored an unrelated NULL key")
	}
	var nullKeys int
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM kv WHERE `key` IS NULL").Scan(&nullKeys); err != nil || nullKeys != 1 {
		t.Fatal("restore rejection lost existing NULL-key data", err)
	}
}

func paddedRevisionTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	// RTRIM reproduces a MySQL PAD SPACE key comparison without a server.
	for _, query := range []string{
		"DROP TABLE kv",
		"CREATE TABLE kv(`key` TEXT COLLATE RTRIM PRIMARY KEY,value TEXT NOT NULL)",
	} {
		if _, err = s.db.ExecContext(context.Background(), query); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func assertPaddedRevisionPreserved(t *testing.T, s *Store) {
	t.Helper()
	var key, value string
	if err := s.db.QueryRowContext(context.Background(), "SELECT `key`,value FROM kv").Scan(&key, &value); err != nil {
		t.Fatal(err)
	}
	if key != postsRevisionKey+" " || value != "preserve-padded-key" {
		t.Errorf("unrelated padded key changed: key=%q value=%q", key, value)
	}
	var count int
	if err := s.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM kv").Scan(&count); err != nil || count != 1 {
		t.Fatal("padded key rejection changed the KV row count", count, err)
	}
}

func TestRestoreRejectsExistingPaddedRevisionKey(t *testing.T) {
	ctx := context.Background()
	s := paddedRevisionTestStore(t)
	empty, err := s.DatabaseSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetValue(ctx, postsRevisionKey+" ", "preserve-padded-key"); err != nil {
		t.Fatal(err)
	}
	if err = s.RestoreDatabase(ctx, empty); err == nil {
		t.Error("SQL collation allowed an unrelated padded key to count as empty")
	}
	assertPaddedRevisionPreserved(t, s)
}

func TestRestorePaddedRevisionBackupRollsBack(t *testing.T) {
	ctx := context.Background()
	s := paddedRevisionTestStore(t)
	p := cachedTestPost(1)
	b := DatabaseBackup{
		Format: "ganwen-blog-database", Version: 1,
		Posts:  []Post{p},
		Values: []DatabaseValue{{Key: postsRevisionKey + " ", Value: "preserve-padded-key"}},
		Jobs: []DatabaseJob{{ID: 1, PostID: p.ID, Language: "en", SourceHash: SourceHash(p),
			Status: "queued", NextAttemptAt: "2026-10-09T00:00:00Z"}},
	}
	if err := s.RestoreDatabase(ctx, b); err == nil {
		t.Error("revision upsert accepted a colliding padded backup key")
	}
	for _, table := range []string{"posts", "kv", "translation_jobs"} {
		var count int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("colliding backup restore partially committed %s: %d rows", table, count)
		}
	}
}

func TestPostWritePaddedRevisionCollisionRollsBack(t *testing.T) {
	ctx := context.Background()
	s := paddedRevisionTestStore(t)
	original := cachedTestPost(1)
	if err := s.saveLocked(ctx, &original); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE kv SET `key`=?,value=? WHERE `key`=?", postsRevisionKey+" ", "preserve-padded-key", postsRevisionKey); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePost(ctx, original.ID, func(p *Post) error {
		p.Title, p.Status = "must roll back", "private"
		return nil
	}); err == nil {
		t.Error("article write accepted a colliding padded revision key")
	}
	stored, err := s.GetPost(ctx, original.ID)
	if err != nil || !reflect.DeepEqual(stored, original) {
		t.Errorf("colliding revision write changed the article: %#v %v", stored, err)
	}
	assertPaddedRevisionPreserved(t, s)
}
