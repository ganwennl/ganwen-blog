package blog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

type Export struct {
	Settings  SiteSettings    `json:"settings"`
	Posts     []Post          `json:"posts"`
	Revisions json.RawMessage `json:"revisions"`
	Media     json.RawMessage `json:"media"`
}

func (s *Store) ImportFile(ctx context.Context, path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	h := sha256.Sum256(raw)
	digest := hex.EncodeToString(h[:])
	old, err := s.GetValue(ctx, "wordpress_import_sha256")
	if err == nil {
		if old == digest {
			return 0, nil
		}
		return 0, errors.New("existing import differs; cannot overwrite managed content")
	}
	if !errors.Is(err, ErrNotFound) {
		return 0, err
	}
	var e Export
	if err = json.Unmarshal(raw, &e); err != nil {
		return 0, err
	}
	if len(e.Posts) == 0 {
		return 0, errors.New("empty WordPress export")
	}
	if err = validateIndependentPagePaths(e.Posts); err != nil {
		return 0, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM posts").Scan(&count); err != nil {
		return 0, err
	}
	if count != 0 {
		return 0, errors.New("destination must be empty")
	}
	for _, p := range e.Posts {
		if p.Version == 0 {
			p.Version = 1
		}
		if p.Translations == nil {
			p.Translations = map[string]Translation{}
		}
		if err = validPost(p); err != nil {
			return 0, fmt.Errorf("post %d: %w", p.ID, err)
		}
		v, err := json.Marshal(p)
		if err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO posts(id,kind,status,published_at,document) VALUES(?,?,?,?,?)", p.ID, p.Type, p.Status, p.PublishedAt, string(v)); err != nil {
			return 0, err
		}
	}
	if e.Settings.PostsPerPage < 1 {
		e.Settings.PostsPerPage = 10
	}
	v, err := json.Marshal(e.Settings)
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO kv(`key`,value) VALUES('site',?),('wordpress_import_sha256',?)", string(v), digest); err != nil {
		return 0, err
	}
	if err = s.markPostsChanged(ctx, tx); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(e.Posts), nil
}
