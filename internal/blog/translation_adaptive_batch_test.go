package blog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func translationStructuralResponse(w http.ResponseWriter, content, reason, refusal string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
		"message": map[string]string{"content": content, "refusal": refusal}, "finish_reason": reason,
	}}})
}

func TestTranslationLongArticlesSplitMalformedBatchesAndKeepEveryFragment(t *testing.T) {
	for _, mode := range []string{"invalid-json", "missing-fragment", "length"} {
		t.Run(mode, func(t *testing.T) {
			s, tr, _ := testTranslator(t)
			var source strings.Builder
			for i := 0; i < 90; i++ {
				fmt.Fprintf(&source, "<p>正文 %d，完整段落。</p>", i)
			}
			p := testTranslationPost(t, s, source.String())
			var calls, smaller atomic.Int32
			testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
				calls.Add(1)
				bytes := 0
				for _, slot := range slots {
					bytes += len(slot.Text) + 100
				}
				if bytes > translationBatchBytes {
					t.Errorf("request exceeded the byte budget: %d", bytes)
				}
				if len(slots) > 4 {
					switch mode {
					case "invalid-json":
						translationStructuralResponse(w, `{"slots":[`, "stop", "")
					case "missing-fragment":
						respondTranslation(w, slots[:len(slots)-1])
					case "length":
						translationStructuralResponse(w, `{"slots":[`, "length", "")
					}
					return
				}
				smaller.Add(1)
				respondTranslation(w, slots)
			})
			if err := tr.Queue(context.Background(), p, false); err != nil {
				t.Fatal(err)
			}
			job := processNextTranslation(t, tr)
			stored, err := s.GetPost(context.Background(), p.ID)
			if err != nil || findTranslationJob(t, tr, job.ID).Status != "completed" {
				t.Fatalf("split translation failed: %+v %v", findTranslationJob(t, tr, job.ID), err)
			}
			if calls.Load() < 3 || smaller.Load() < 2 || calls.Load() > 200 {
				t.Fatalf("unexpected split call count: %d, successful children: %d", calls.Load(), smaller.Load())
			}
			translated := stored.Translations[job.Language]
			if strings.Count(translated.ContentHTML, "<p>") != 90 {
				t.Fatal("splitting omitted or duplicated an article paragraph")
			}
			for i := 0; i < 90; i++ {
				if !strings.Contains(translated.ContentHTML, fmt.Sprintf("Body %d，完整段落。", i)) {
					t.Fatalf("paragraph %d missing after split", i)
				}
			}
			if stored.ContentHTML != p.ContentHTML || stored.Status != "publish" || translated.Status != "review" {
				t.Fatal("splitting changed original content, visibility or review policy")
			}
		})
	}
}

func TestTranslationBatchSplittingDoesNotRetryHTTPFailuresOrRefusals(t *testing.T) {
	for _, mode := range []string{"401", "429", "500", "503", "refusal", "content-filter"} {
		t.Run(mode, func(t *testing.T) {
			s, tr, _ := testTranslator(t)
			p := testTranslationPost(t, s, "<p>正文</p><p>其他段落</p>")
			var calls atomic.Int32
			testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
				calls.Add(1)
				if mode == "refusal" {
					translationStructuralResponse(w, "", "stop", "provider refusal")
				} else if mode == "content-filter" {
					translationStructuralResponse(w, "", "content_filter", "")
				} else {
					status := 0
					_, _ = fmt.Sscanf(mode, "%d", &status)
					w.WriteHeader(status)
				}
			})
			_, err := tr.translate(context.Background(), p, "en", tr.secretConfiguration())
			if err == nil || calls.Load() != 1 {
				t.Fatalf("non-structural failure fanned out: %d calls, %v", calls.Load(), err)
			}
			stored, _ := s.GetPost(context.Background(), p.ID)
			if len(stored.Translations) != 0 {
				t.Fatal("failed request saved partial text")
			}
		})
	}
}

func TestTranslationStructuralSplitChecksCancellationBeforeEveryChild(t *testing.T) {
	s, tr, _ := testTranslator(t)
	p := testTranslationPost(t, s, "<p>正文</p><p>其他段落</p>")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		calls.Add(1)
		translationStructuralResponse(w, `{"slots":[]}`, "stop", "")
	})
	guardCalls := 0
	guard := func() error {
		guardCalls++
		if calls.Load() != 0 {
			cancel()
			return context.Cause(ctx)
		}
		return nil
	}
	_, err := tr.translateGuarded(ctx, p, "en", tr.secretConfiguration(), guard)
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 || guardCalls < 2 {
		t.Fatalf("cancelled split made another request: calls=%d guards=%d err=%v", calls.Load(), guardCalls, err)
	}
}

func TestTranslationStructuralSplitChecksSourceBaselineBeforeEveryChild(t *testing.T) {
	s, tr, _ := testTranslator(t)
	p := testTranslationPost(t, s, "<p>旧正文</p><p>其他段落</p>")
	var calls atomic.Int32
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		calls.Add(1)
		if err := s.UpdatePost(context.Background(), p.ID, func(p *Post) error {
			p.ContentHTML = "<p>原文在请求中改变</p>"
			return nil
		}); err != nil {
			t.Error(err)
		}
		translationStructuralResponse(w, `{"slots":[]}`, "stop", "")
	})
	if err := tr.Queue(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	job := processNextTranslation(t, tr)
	stored, _ := s.GetPost(context.Background(), p.ID)
	if calls.Load() != 1 || findTranslationJob(t, tr, job.ID).Status != "cancelled" || len(stored.Translations) != 0 {
		t.Fatal("source changed before recursive split but another provider call or commit occurred")
	}
}

func TestTranslationSingleSlotStructuralFailureHasFiniteRetries(t *testing.T) {
	_, tr, _ := testTranslator(t)
	var calls atomic.Int32
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		calls.Add(1)
		translationStructuralResponse(w, `{"slots":[]}`, "length", "")
	})
	_, err := tr.translateBatchGuarded(context.Background(), []translationSlot{{ID: "one", Text: "正文"}}, "en", tr.secretConfiguration(), nil, 0)
	if err == nil || calls.Load() != 1 {
		t.Fatalf("single-slot failure did not stop: %d %v", calls.Load(), err)
	}
}

func TestTranslationKeepExpansionCannotCreateOversizedSingleSlots(t *testing.T) {
	source := strings.Repeat("中文 1 2 3 4 5 6 7 8 9 ", 600)
	doc, err := newTranslationDocument(source, true, "dense", "")
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string, len(doc.slots))
	for _, slot := range doc.slots {
		if len(slot.Text)+200 > translationBatchBytes {
			t.Fatalf("protected-number expansion exceeded slot budget: %d bytes", len(slot.Text))
		}
		values[slot.ID] = slot.Text
	}
	rendered, err := doc.render(values)
	if err != nil || rendered != source {
		t.Fatal("size-limited slots lost source text or protected number occurrences")
	}
}
