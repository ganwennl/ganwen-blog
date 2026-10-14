package blog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

// The workbench reuses the production text-slot validator for the initial migration.
// It lets initial translations be prepared without sharing a translation API key.
type OfflineTask struct {
	PostID     int64             `json:"post_id"`
	SourceHash string            `json:"source_hash"`
	Language   string            `json:"language"`
	Slots      []translationSlot `json:"slots"`
	Values     map[string]string `json:"values,omitempty"`
}

func offlineDocuments(p Post) ([]*translationDocument, error) {
	out := []*translationDocument{}
	for _, x := range []struct {
		value  string
		plain  bool
		prefix string
	}{{p.Title, true, "title"}, {p.ContentHTML, false, "body"}, {p.ExcerptHTML, false, "excerpt"}} {
		d, err := newTranslationDocument(x.value, x.plain, x.prefix, p.Author)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}
func ExportOfflineTasks(ctx context.Context, s *Store, path string) error {
	return exportOfflineTasks(ctx, s, path, false)
}
func ExportAllOfflineTasks(ctx context.Context, s *Store, path string) error {
	return exportOfflineTasks(ctx, s, path, true)
}
func exportOfflineTasks(ctx context.Context, s *Store, path string, includePrivate bool) error {
	posts, err := s.ListPosts(ctx, includePrivate)
	if err != nil {
		return err
	}
	tasks := []OfflineTask{}
	for _, p := range posts {
		if p.Type != "post" && p.Type != "page" {
			continue
		}
		docs, err := offlineDocuments(p)
		if err != nil {
			return err
		}
		slots := []translationSlot{}
		for _, d := range docs {
			slots = append(slots, d.slots...)
		}
		hash := SourceHash(p)
		for _, lang := range translationLanguages() {
			tasks = append(tasks, OfflineTask{PostID: p.ID, SourceHash: hash, Language: lang, Slots: slots})
		}
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].PostID == tasks[j].PostID {
			return tasks[i].Language < tasks[j].Language
		}
		return tasks[i].PostID < tasks[j].PostID
	})
	raw, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0600)
}
func ImportOfflineTasks(ctx context.Context, s *Store, path string, publish bool) (int, error) {
	return ImportOfflineTasksWithOptions(ctx, s, path, publish, false)
}
func ImportOfflineTasksWithOptions(ctx context.Context, s *Store, path string, publish, replaceMachine bool) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var tasks []OfflineTask
	if err = json.Unmarshal(raw, &tasks); err != nil {
		return 0, err
	}
	seen := map[string]bool{}
	n := 0
	for _, task := range tasks {
		if !isTranslationLanguage(task.Language) {
			return n, errors.New("unsupported language")
		}
		key := fmt.Sprintf("%d/%s", task.PostID, task.Language)
		if seen[key] {
			return n, errors.New("duplicate translation")
		}
		seen[key] = true
		err = s.UpdatePost(ctx, task.PostID, func(p *Post) error {
			if (p.Status != "publish" && p.Status != "draft" && p.Status != "private" && p.Status != "pending") || SourceHash(*p) != task.SourceHash {
				return ErrConflict
			}
			docs, err := offlineDocuments(*p)
			if err != nil {
				return err
			}
			expected := 0
			for _, d := range docs {
				expected += len(d.slots)
			}
			if len(task.Values) != expected {
				return fmt.Errorf("%s: missing or extra translated slots", key)
			}
			rendered := []string{}
			for _, d := range docs {
				v, err := d.render(task.Values)
				if err != nil {
					return fmt.Errorf("%s: %w", key, err)
				}
				rendered = append(rendered, v)
			}
			status := "review"
			if publish && p.Status == "publish" {
				status = "publish"
			}
			tr := Translation{Title: rendered[0], ContentHTML: rendered[1], ExcerptHTML: rendered[2], SourceHash: task.SourceHash, Status: status, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
			tr.MachineHash = offlineMachineHash(tr)
			if p.Translations == nil {
				p.Translations = map[string]Translation{}
			}
			if existing, ok := p.Translations[task.Language]; ok {
				if !replaceMachine || existing.MachineHash == "" || existing.MachineHash != TranslationContentHash(existing) {
					return errors.New("existing or manually edited translation requires explicit review")
				}
			}
			p.Translations[task.Language] = tr
			return nil
		})
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// Same content tuple as the translation worker's machine-origin fingerprint.
func offlineMachineHash(v Translation) string { return TranslationContentHash(v) }
