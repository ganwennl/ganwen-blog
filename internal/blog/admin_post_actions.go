package blog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var errPostActionInvalid = errors.New("此内容当前不能执行该操作")
var errIndependentPageMove = errors.New("独立页面不能直接移动，请先在编辑中清空访问地址")

// Quick list actions. Every action carries the list's version so a stale
// click cannot overwrite a newer edit; deleting is two-step via the trash.
const (
	postActionPublish = "publish"
	postActionHide    = "hide"
	postActionTrash   = "trash"
	postActionRestore = "restore"
	postActionDelete  = "delete"
	postActionToPost  = "to_post"
	postActionToPage  = "to_page"
)

func (a *Admin) postAction(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID      int64  `json:"id"`
		Version int64  `json:"version"`
		Action  string `json:"action"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	if input.ID < 1 {
		adminError(w, 400, "文章编号不正确")
		return
	}
	switch input.Action {
	case postActionPublish, postActionHide, postActionTrash, postActionRestore, postActionDelete, postActionToPost, postActionToPage:
	default:
		adminError(w, 400, "操作不正确")
		return
	}
	if input.Action == postActionDelete {
		if err := a.app.Store.DeleteTrashedPost(r.Context(), input.ID, input.Version); err != nil {
			postActionError(w, err)
			return
		}
		adminJSON(w, 200, map[string]any{"deleted": true, "id": input.ID})
		return
	}
	p, err := a.app.Store.ApplyPostAction(r.Context(), input.ID, input.Version, input.Action)
	if err != nil {
		postActionError(w, err)
		return
	}
	warning := ""
	if input.Action == postActionPublish && a.app.Translator != nil {
		if err := a.app.Translator.QueueAutomatic(r.Context(), p); err != nil {
			warning = "已公开，翻译排队失败，请稍后重新加入队列"
		}
	}
	result := adminPostResponse(p)
	result["warning"] = warning
	adminJSON(w, 200, result)
}

func postActionError(w http.ResponseWriter, err error) {
	if errors.Is(err, errPostActionInvalid) || errors.Is(err, errIndependentPageMove) {
		adminError(w, 409, err.Error())
		return
	}
	storeError(w, err)
}

// ApplyPostAction changes status or type in one versioned write.
func (s *Store) ApplyPostAction(ctx context.Context, id, version int64, action string) (Post, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	p, err := s.GetPost(ctx, id)
	if err != nil {
		return Post{}, err
	}
	if p.Version != version {
		return Post{}, ErrConflict
	}
	if p.Type != "post" && p.Type != "page" {
		return Post{}, errPostActionInvalid
	}
	trashed := p.Status == "trash"
	switch action {
	case postActionPublish:
		if trashed {
			return Post{}, errPostActionInvalid
		}
		p.Status = "publish"
	case postActionHide:
		if trashed {
			return Post{}, errPostActionInvalid
		}
		p.Status = "private"
	case postActionTrash:
		if trashed {
			return Post{}, errPostActionInvalid
		}
		p.Status = "trash"
	case postActionRestore:
		if !trashed {
			return Post{}, errPostActionInvalid
		}
		// Restored content stays hidden until it is explicitly published again.
		p.Status = "draft"
	case postActionToPost, postActionToPage:
		target := "post"
		if action == postActionToPage {
			target = "page"
		}
		if trashed || p.Type == target {
			return Post{}, errPostActionInvalid
		}
		if p.PublicPath != "" || p.Unlisted {
			// Moving would drop the custom address and list an address-only page publicly.
			return Post{}, errIndependentPageMove
		}
		if err = s.convertPostType(ctx, &p, target); err != nil {
			return Post{}, err
		}
	default:
		return Post{}, errPostActionInvalid
	}
	p.ModifiedAt = time.Now().UTC().Format(time.RFC3339)
	if err = validPost(p); err != nil {
		return Post{}, err
	}
	p.Version++
	if err = s.saveLocked(ctx, &p); err != nil {
		return Post{}, err
	}
	return p, nil
}

// convertPostType moves content between the article and page groups. Only
// generated ID addresses are rewritten; imported WordPress permalinks stay.
func (s *Store) convertPostType(ctx context.Context, p *Post, target string) error {
	settings, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	base := strings.TrimRight(settings.BaseURL, "/")
	oldKey, newKey := "page_id", "p"
	if target == "page" {
		oldKey, newKey = "p", "page_id"
	}
	generated := p.CanonicalURL == "" || p.CanonicalURL == fmt.Sprintf("%s/?%s=%d", base, oldKey, p.ID) || isLocalhostGeneratedCanonical(p.CanonicalURL, p.ID, oldKey)
	p.Type = target
	// Old ?page_id= links of a moved page redirect to its article address.
	p.MovedFromPage = target == "post"
	if generated {
		p.CanonicalURL = fmt.Sprintf("%s/?%s=%d", base, newKey, p.ID)
	}
	return nil
}

// DeleteTrashedPost permanently removes content already in the trash.
func (s *Store) DeleteTrashedPost(ctx context.Context, id, version int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	p, err := s.GetPost(ctx, id)
	if err != nil {
		return err
	}
	if p.Version != version {
		return ErrConflict
	}
	if p.Status != "trash" || (p.Type != "post" && p.Type != "page") {
		return errPostActionInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM posts WHERE id=?", id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM translation_jobs WHERE post_id=?", id); err != nil {
		return err
	}
	if err = s.markPostsChanged(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
