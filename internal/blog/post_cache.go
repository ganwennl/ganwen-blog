package blog

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// Every application write to posts changes this token in the same transaction.
// Readers check it in the database on every call, including across Store/process
// boundaries. Random tokens also prevent a restore from reviving an old cache.
const postsRevisionKey = "posts_revision_v1"

type postSnapshot struct {
	revision     string
	posts        []Post
	published    []bool
	public       []bool
	publicCount  int
	summaryCount int
	hashes       map[int64]string
}

func newPostsRevision() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

// markPostsChanged must run before committing any transaction that writes posts.
func (s *Store) markPostsChanged(ctx context.Context, tx *sql.Tx) error {
	revision, err := newPostsRevision()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.kvUpsertSQL(), postsRevisionKey, revision)
	if err != nil {
		return err
	}
	// MySQL PAD SPACE and legacy SQLite collations can match a different
	// key. Refuse that collision before committing the article transaction;
	// rollback also preserves the unrelated key's original value.
	var key string
	if err = tx.QueryRowContext(ctx, "SELECT `key` FROM kv WHERE `key`=?", postsRevisionKey).Scan(&key); err != nil {
		return err
	}
	if key != postsRevisionKey {
		return errors.New("article cache revision key collides with existing configuration")
	}
	return nil
}

func (s *Store) postSnapshot(ctx context.Context) (*postSnapshot, error) {
	// The query is deliberately outside the mutex: parallel warm readers do not
	// queue behind a snapshot copy, and database errors never serve cached data.
	revision, err := s.GetValue(ctx, postsRevisionKey)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	s.postsMu.Lock()
	defer s.postsMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.posts != nil && s.posts.revision == revision {
		return s.posts, nil
	}
	// Both the token and documents come from one consistent snapshot. A writer
	// committing during this rebuild cannot label old content with its new token.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var fresh postSnapshot
	if err = tx.QueryRowContext(ctx, "SELECT value FROM kv WHERE `key`=?", postsRevisionKey).Scan(&fresh.revision); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT status,document FROM posts ORDER BY published_at DESC,id DESC")
	if err != nil {
		return nil, err
	}
	fresh.posts = []Post{}
	fresh.hashes = make(map[int64]string)
	for rows.Next() {
		var status, raw string
		var p Post
		if err = rows.Scan(&status, &raw); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			break
		}
		fresh.posts = append(fresh.posts, p)
		// Preserve the status column's publication boundary and fail closed if
		// legacy/manual data disagrees with its JSON document.
		published := status == "publish" && p.Status == "publish"
		fresh.published = append(fresh.published, published)
		public := published && !p.Unlisted
		fresh.public = append(fresh.public, public)
		if public {
			fresh.publicCount++
		}
		if p.Type == "post" || p.Type == "page" {
			fresh.summaryCount++
		}
		fresh.hashes[p.ID] = SourceHash(p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.posts = &fresh
	return s.posts, nil
}

// All mutable members are copied. Strings and value-only structs can be shared;
// callers may safely edit returned terms, translations, slices and maps.
func clonePost(p Post, summary bool) Post {
	if p.Categories != nil {
		p.Categories = append([]Term{}, p.Categories...)
	}
	if p.Tags != nil {
		p.Tags = append([]Term{}, p.Tags...)
	}
	if p.Translations != nil {
		translations := make(map[string]Translation, len(p.Translations))
		for lang, t := range p.Translations {
			if summary {
				t.ContentHTML, t.ExcerptHTML = "", ""
			}
			translations[lang] = t
		}
		p.Translations = translations
	}
	if summary {
		p.ContentHTML, p.SourceHTML, p.ExcerptHTML = "", "", ""
	}
	return p
}

// ListPostSummaries preserves the admin API shape while copying only article
// metadata from the shared snapshot. Full bodies remain available via GetPost.
func (s *Store) ListPostSummaries(ctx context.Context) ([]Post, error) {
	snapshot, err := s.postSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	posts := make([]Post, 0, snapshot.summaryCount)
	for _, p := range snapshot.posts {
		if p.Type == "post" || p.Type == "page" {
			posts = append(posts, clonePost(p, true))
		}
	}
	return posts, nil
}

func (s *Store) ListAdminPosts(ctx context.Context) ([]Post, error) {
	return s.ListPostSummaries(ctx)
}

func (s *Store) PostSourceHashes(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	snapshot, err := s.postSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if hash, ok := snapshot.hashes[id]; ok {
			out[id] = hash
		}
	}
	return out, nil
}
