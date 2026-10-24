package blog

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTranslationAuthorProtectionDoesNotSplitLongerChineseNames(t *testing.T) {
	for _, plain := range []bool{true, false} {
		source := "關於甘文； 甘 的日记；甘文的介绍；文甘；甘A；A甘。"
		if !plain {
			source = "<p>" + source + "</p>"
		}
		doc, err := newTranslationDocument(source, plain, "fixture", "甘")
		if err != nil || len(doc.slots) != 1 {
			t.Fatalf("document: %+v %v", doc, err)
		}
		text := doc.slots[0].Text
		if !strings.Contains(text, "關於甘文") || !strings.Contains(text, "甘文的介绍") || !strings.Contains(text, "文甘") {
			t.Fatalf("author prefix split a longer name: %s", text)
		}
		if !strings.Contains(text, "； ⟪GW_KEEP_0⟫ 的日记") || strings.Contains(text, "⟫文") {
			t.Fatalf("standalone author protection: %s", text)
		}
		got, err := doc.render(map[string]string{doc.slots[0].ID: text})
		if err != nil || got != source {
			t.Fatalf("roundtrip changed names or markup: %s %v", got, err)
		}
	}
}

func TestTranslationQueueResultProtectsStaleManualTranslationsAndAllowsExplicitForce(t *testing.T) {
	s, tr, _ := testTranslator(t)
	ctx := context.Background()
	p := testTranslationPost(t, s, "<p>旧的关于页面</p>")
	if err := tr.Queue(ctx, p, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE translation_jobs SET status='completed'"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePost(ctx, p.ID, func(p *Post) error {
		for _, language := range translationLanguages() {
			p.Translations[language] = Translation{Title: "Reviewed " + language, ContentHTML: "<p>Original review</p>", Status: "publish", SourceHash: SourceHash(*p)}
		}
		p.ContentHTML = "<p>新的关于页面</p>"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetPost(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tr.QueueWithResult(ctx, p, false)
	if err != nil || !result.OK || result.Queued != 0 || result.Active != 0 || result.Skipped != 10 || result.Protected != 10 {
		t.Fatalf("stale manual queue result: %+v %v", result, err)
	}
	for _, language := range translationLanguages() {
		if result.Languages[language] != "protected" {
			t.Fatalf("%s not reported as protected", language)
		}
	}
	jobs, err := tr.Status(ctx)
	if err != nil || len(jobs) != 10 {
		t.Fatalf("historical jobs: %+v %v", jobs, err)
	}
	for _, job := range jobs {
		if job.Status != "completed" || job.SourceHash == SourceHash(before) {
			t.Fatal("normal request changed historical completed tasks")
		}
	}
	result, err = tr.QueueWithResult(ctx, p, true)
	if err != nil || result.Queued != 10 || result.Skipped != 0 || result.Protected != 0 {
		t.Fatalf("explicit retranslation result: %+v %v", result, err)
	}
	result, err = tr.QueueWithResult(ctx, p, true)
	if err != nil || result.Queued != 0 || result.Active != 10 {
		t.Fatalf("duplicate active retranslation: %+v %v", result, err)
	}
	after, err := s.GetPost(ctx, p.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("queueing changed source, visibility or existing translations")
	}
}

func TestTranslationQueueResultCountsCurrentProtectedMachineAndMissingLanguages(t *testing.T) {
	s, tr, _ := testTranslator(t)
	ctx := context.Background()
	p := testTranslationPost(t, s, "<p>正文</p>")
	if err := s.UpdatePost(ctx, p.ID, func(p *Post) error {
		p.Translations["nl"] = Translation{Title: "Manual", SourceHash: "old", Status: "review"}
		p.Translations["en"] = Translation{Title: "Edited machine", SourceHash: "old", MachineHash: "different", Status: "review"}
		p.Translations["hi"] = Translation{Title: "Current", SourceHash: SourceHash(*p), Status: "review"}
		machine := Translation{Title: "Old machine", SourceHash: "old", Status: "review"}
		machine.MachineHash = TranslationContentHash(machine)
		p.Translations["es"] = machine
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first, err := tr.QueueWithResult(ctx, p, false)
	if err != nil || first.Queued != 7 || first.Active != 0 || first.Protected != 2 || first.UpToDate != 1 || first.Skipped != 3 || first.Languages["es"] != "queued" {
		t.Fatalf("mixed first result: %+v %v", first, err)
	}
	second, err := tr.QueueWithResult(ctx, p, false)
	if err != nil || second.Queued != 0 || second.Active != 7 || second.Skipped != 3 {
		t.Fatalf("mixed repeated result: %+v %v", second, err)
	}
}

func TestTranslationQueueResultReportsFailedJobsUntilExplicitRetry(t *testing.T) {
	s, tr, _ := testTranslator(t)
	ctx := context.Background()
	p := testTranslationPost(t, s, "<p>正文</p>")
	if err := tr.Queue(ctx, p, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE translation_jobs SET status='failed',attempts=5"); err != nil {
		t.Fatal(err)
	}
	result, err := tr.QueueWithResult(ctx, p, false)
	if err != nil || result.Queued != 0 || result.Active != 0 || result.Failed != 10 || result.Skipped != 10 {
		t.Fatalf("failed result: %+v %v", result, err)
	}
	result, err = tr.QueueWithResult(ctx, p, true)
	if err != nil || result.Queued != 10 || result.Failed != 0 || result.Skipped != 0 {
		t.Fatalf("explicit retry: %+v %v", result, err)
	}
	jobs, _ := tr.Status(ctx)
	for _, job := range jobs {
		if job.Status != "queued" || job.Attempts != 0 {
			t.Fatal("explicit retry did not reset terminal failures")
		}
	}
}

func TestTranslationQueueResultReportsIneligibleSourcesWithoutQueueing(t *testing.T) {
	for _, status := range []string{"draft", "private", "pending", "future", "no-chinese"} {
		t.Run(status, func(t *testing.T) {
			s, tr, _ := testTranslator(t)
			ctx := context.Background()
			p := testTranslationPost(t, s, "<p>正文</p>")
			wantReason := "not_public"
			if err := s.UpdatePost(ctx, p.ID, func(p *Post) error {
				if status == "no-chinese" {
					p.Title, p.ContentHTML, p.ExcerptHTML = "English", "<p>Body</p>", ""
					wantReason = "no_chinese"
				} else {
					p.Status = status
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			for _, force := range []bool{false, true} {
				result, err := tr.QueueWithResult(ctx, p, force)
				if err != nil || result.Queued != 0 || result.Active != 0 || result.Skipped != 10 || result.Reason != wantReason {
					t.Fatalf("ineligible force=%v: %+v %v", force, result, err)
				}
			}
			jobs, _ := tr.Status(ctx)
			if len(jobs) != 0 {
				t.Fatal("ineligible article created translation jobs")
			}
		})
	}
}

func TestAdminTranslationQueueReturnsActualProtectedCountsAndRejectsMissingService(t *testing.T) {
	c := adminTestSetup(t, true)
	c.login()
	p := c.newPost("publish")
	code, _ := c.request("POST", "translate", map[string]any{"id": p.ID}, c.server.URL, c.csrf)
	if code != 400 {
		t.Fatal("unconfigured service accepted a translation request")
	}
	jobs, _ := c.app.Translator.Status(context.Background())
	if len(jobs) != 0 {
		t.Fatal("unconfigured service created jobs")
	}
	c.app.Translator.cfgMu.Lock()
	c.app.Translator.cfg = TranslationConfig{Provider: "compatible", Endpoint: "https://api.example.test/v1", Model: "fixture-model", APIKey: "fixture-secret", ExecutionMode: translationModeManual}
	c.app.Translator.cfgMu.Unlock()
	if err := c.app.Store.UpdatePost(context.Background(), p.ID, func(p *Post) error {
		for _, language := range translationLanguages() {
			p.Translations[language] = Translation{Title: "Manual " + language, SourceHash: "old", Status: "review"}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, body := c.request("POST", "translate", map[string]any{"id": p.ID, "force": false}, c.server.URL, c.csrf)
	var result TranslationQueueResult
	if code != 200 || json.Unmarshal(body, &result) != nil || !result.OK || result.Queued != 0 || result.Protected != 10 || result.Skipped != 10 {
		t.Fatalf("API protected result: %d %s", code, body)
	}
	code, body = c.request("POST", "translate", map[string]any{"id": p.ID, "force": true}, c.server.URL, c.csrf)
	if code != 200 || json.Unmarshal(body, &result) != nil || result.Queued != 10 || result.Protected != 0 {
		t.Fatalf("API force result: %d %s", code, body)
	}
	private := c.newPost("private")
	code, body = c.request("POST", "translate", map[string]any{"id": private.ID, "force": true}, c.server.URL, c.csrf)
	if code != 200 || json.Unmarshal(body, &result) != nil || result.Reason != "not_public" || result.Queued != 0 || result.Skipped != 10 {
		t.Fatalf("API private result: %d %s", code, body)
	}
}
