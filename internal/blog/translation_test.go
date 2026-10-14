package blog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testTranslator(t *testing.T) (*Store, *Translator, Config) {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(filepath.Join(dir, "blog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	cfg := Config{DataDir: dir}
	tr, err := NewTranslator(s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Configuration tests resolve deterministic public addresses without DNS or paid services.
	tr.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	return s, tr, cfg
}

func testTranslationPost(t *testing.T, s *Store, content string) Post {
	t.Helper()
	p := Post{Type: "post", Status: "publish", Title: "你好甘", ContentHTML: content, ExcerptHTML: "<p>正文摘要</p>", Author: "甘", SEO: SEO{Title: "你好", Description: "正文介绍"}}
	if err := s.NewPost(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func testTranslationServer(t *testing.T, tr *Translator, handler func(http.ResponseWriter, *http.Request, []translationSlot)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-not-a-real-key" {
			t.Errorf("invalid translation request: %s", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) != 2 {
			t.Errorf("invalid request payload")
			w.WriteHeader(400)
			return
		}
		var input struct {
			Slots  []translationSlot `json:"slots"`
			Target string            `json:"target_language"`
		}
		if err := json.Unmarshal([]byte(body.Messages[1].Content), &input); err != nil {
			t.Errorf("invalid translation slots")
			w.WriteHeader(400)
			return
		}
		r.Header.Set("X-Test-Translation-Target", input.Target)
		handler(w, r, input.Slots)
	}))
	t.Cleanup(server.Close)
	tr.allowLoopbackHTTP = true
	if err := tr.Configure(context.Background(), TranslationConfig{Endpoint: server.URL + "/v1", Model: "local-test", APIKey: "test-not-a-real-key"}); err != nil {
		t.Fatal(err)
	}
	return server
}

func respondTranslation(w http.ResponseWriter, slots []translationSlot) {
	result := struct {
		Slots []translationSlot `json:"slots"`
	}{Slots: make([]translationSlot, len(slots))}
	for i, s := range slots {
		result.Slots[i] = translationSlot{ID: s.ID, Text: strings.NewReplacer("你好", "Hello", "正文", "Body", "摘要", " summary", "介绍", " introduction", "图片", "Image", "访问", "Visit", "价格", "Price", "姓名", "Name", "中文", "Chinese").Replace(s.Text)}
	}
	b, _ := json.Marshal(result)
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(b)}, "finish_reason": "stop"}}})
}

func processNextTranslation(t *testing.T, tr *Translator) claimedTranslationJob {
	t.Helper()
	j, err := tr.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tr.process(context.Background(), j, tr.secretConfiguration())
	return j
}

func findTranslationJob(t *testing.T, tr *Translator, id int64) TranslationJob {
	t.Helper()
	var j TranslationJob
	err := tr.store.db.QueryRowContext(context.Background(), `SELECT id,post_id,language,status,last_error,attempts,source_hash,next_attempt_at FROM translation_jobs WHERE id=?`, id).Scan(&j.ID, &j.PostID, &j.Language, &j.Status, &j.LastError, &j.Attempts, &j.SourceHash, &j.NextAttemptAt)
	if err != nil {
		t.Fatalf("missing persisted job %d: %v", id, err)
	}
	return j
}

func TestTranslationConfigurationEncryptsAndMasksKey(t *testing.T) {
	s, tr, cfg := testTranslator(t)
	ctx := context.Background()
	const key = "only-a-test-secret"
	if err := tr.Configure(ctx, TranslationConfig{Endpoint: "https://translation.example/v1", Model: "test", APIKey: key}); err != nil {
		t.Fatal(err)
	}
	saved, err := s.GetValue(ctx, translationConfigKey)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(saved, key) {
		t.Fatal("plaintext API key persisted")
	}
	var encrypted persistedTranslationConfig
	if err := json.Unmarshal([]byte(saved), &encrypted); err != nil {
		t.Fatal(err)
	}
	if encrypted.EncryptedAPIKey == "" {
		t.Fatal("missing encrypted key")
	}
	read, err := tr.Configuration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.APIKey != "" || !read.HasAPIKey {
		t.Fatal("configuration exposed API key or lost configured state")
	}
	if err := tr.Configure(ctx, TranslationConfig{Endpoint: read.Endpoint, Model: "changed", APIKey: ""}); err != nil {
		t.Fatal(err)
	}
	if tr.secretConfiguration().APIKey != key {
		t.Fatal("empty key erased existing secret")
	}
	reloaded, err := NewTranslator(s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.secretConfiguration().APIKey != key {
		t.Fatal("key did not survive restart")
	}
	info, err := os.Stat(filepath.Join(cfg.DataDir, "translation.key"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 32 {
		t.Fatal("invalid master key")
	}
	if err := tr.Configure(ctx, TranslationConfig{Endpoint: "http://127.0.0.1/v1", Model: "test"}); err == nil {
		t.Fatal("production accepted HTTP endpoint")
	}
	if err := tr.Configure(ctx, TranslationConfig{Endpoint: "https://user:password@example.com/v1", Model: "test"}); err == nil {
		t.Fatal("endpoint accepted URL credentials")
	}
}

func TestTranslationProviderDefaultsAndServiceKeyIsolation(t *testing.T) {
	s, tr, cfg := testTranslator(t)
	ctx := context.Background()
	if err := tr.Configure(ctx, TranslationConfig{Provider: "openai", Model: "openai-test-model", APIKey: "openai-test-key"}); err != nil {
		t.Fatal(err)
	}
	before := tr.secretConfiguration()
	if before.Provider != "openai" || before.Endpoint != "https://api.openai.com/v1" {
		t.Fatal("OpenAI preset was not applied")
	}
	rawBefore, _ := s.GetValue(ctx, translationConfigKey)
	if err := tr.Configure(ctx, TranslationConfig{Provider: "deepseek"}); err == nil {
		t.Fatal("switching service reused the previous service key")
	}
	rawAfter, _ := s.GetValue(ctx, translationConfigKey)
	if tr.secretConfiguration() != before || rawBefore != rawAfter {
		t.Fatal("rejected service switch modified the saved configuration")
	}
	if err := tr.Configure(ctx, TranslationConfig{Provider: "deepseek", APIKey: "deepseek-test-key"}); err != nil {
		t.Fatal(err)
	}
	read, err := tr.Configuration(ctx)
	if err != nil || read.Provider != "deepseek" || read.Endpoint != translationDeepSeekEndpoint || read.Model != translationDeepSeekModel || !read.HasAPIKey || read.APIKey != "" {
		t.Fatal("DeepSeek preset or secret masking failed")
	}
	read.Model = "deepseek-v4-pro"
	read.Endpoint += "/chat/completions/"
	if err := tr.Configure(ctx, read); err != nil {
		t.Fatal(err)
	}
	if tr.secretConfiguration().APIKey != "deepseek-test-key" {
		t.Fatal("same service discarded the encrypted key")
	}
	for _, changed := range []TranslationConfig{
		{Provider: "compatible", Endpoint: read.Endpoint, Model: read.Model},
		{Provider: "deepseek", Endpoint: "https://another-service.example/v1", Model: read.Model},
		{Provider: "unsupported", Endpoint: read.Endpoint, Model: read.Model, APIKey: "test-key"},
	} {
		if err := tr.Configure(ctx, changed); err == nil {
			t.Fatal("unsafe or invalid service switch was accepted")
		}
	}
	saved, _ := s.GetValue(ctx, translationConfigKey)
	if strings.Contains(saved, "deepseek-test-key") || !strings.Contains(saved, `"provider":"deepseek"`) {
		t.Fatal("provider persistence or key encryption failed")
	}
	reloaded, err := NewTranslator(s, cfg)
	if err != nil || reloaded.secretConfiguration().Provider != "deepseek" || reloaded.secretConfiguration().APIKey != "deepseek-test-key" || reloaded.secretConfiguration().Model != "deepseek-v4-pro" {
		t.Fatal("provider configuration did not survive restart")
	}
}

func TestTranslationLegacyProviderInferenceKeepsConfiguration(t *testing.T) {
	for _, test := range []struct{ endpoint, model, provider string }{
		{"https://api.openai.com/v1", "existing-openai-model", "openai"},
		{"https://api.deepseek.com/v1", "existing-deepseek-model", "deepseek"},
		{"https://custom.example/v1", "existing-custom-model", "compatible"},
		{"", "", "openai"},
		{"https://api.deepseek.com", "", "deepseek"},
	} {
		t.Run(test.endpoint+test.model, func(t *testing.T) {
			s, tr, cfg := testTranslator(t)
			encrypted, err := tr.encrypt("legacy-test-key")
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"endpoint": test.endpoint, "model": test.model, "encrypted_api_key": encrypted, "auto_publish": false})
			if err := s.SetValue(context.Background(), translationConfigKey, string(raw)); err != nil {
				t.Fatal(err)
			}
			reloaded, err := NewTranslator(s, cfg)
			if err != nil {
				t.Fatal(err)
			}
			got := reloaded.secretConfiguration()
			if got.Provider != test.provider || got.Endpoint != test.endpoint || got.Model != test.model || got.APIKey != "legacy-test-key" || got.AutoPublish || got.ExecutionMode != translationModeAutomatic {
				t.Fatal("legacy configuration changed instead of inferring its provider")
			}
		})
	}
}

func TestTranslationPausedModePreservesConfigurationAndBlocksAllRequests(t *testing.T) {
	s, tr, cfg := testTranslator(t)
	ctx := context.Background()
	p := testTranslationPost(t, s, "<p>正文</p>")
	var calls atomic.Int32
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		calls.Add(1)
		respondTranslation(w, slots)
	})
	if err := tr.Queue(ctx, p, false); err != nil {
		t.Fatal(err)
	}
	before, _ := tr.Status(ctx)
	activeConfig := tr.secretConfiguration()
	paused, _ := tr.Configuration(ctx)
	paused.ExecutionMode = translationModePaused
	if err := tr.Configure(ctx, paused); err != nil {
		t.Fatal(err)
	}
	if got := tr.secretConfiguration(); got.APIKey != activeConfig.APIKey || got.Endpoint != activeConfig.Endpoint || got.Model != activeConfig.Model || got.ExecutionMode != translationModePaused {
		t.Fatal("pausing changed the saved translation service or key")
	}
	legacyUpdate := paused
	legacyUpdate.ExecutionMode = ""
	if err := tr.Configure(ctx, legacyUpdate); err != nil || tr.secretConfiguration().ExecutionMode != translationModePaused {
		t.Fatal("older admin configuration save silently resumed external translation")
	}
	if err := tr.QueueAutomatic(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := tr.Queue(ctx, p, true); !errors.Is(err, errTranslationDisabled) {
		t.Fatal("paused external translation accepted forced generation")
	}
	noTranslationReady(t, tr)
	if _, err := tr.translateBatch(ctx, []translationSlot{{ID: "title", Text: "你好"}}, "en", activeConfig); !errors.Is(err, errTranslationDisabled) {
		t.Fatal("stale automatic configuration bypassed pause")
	}
	workerCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	tr.Run(workerCtx)
	after, _ := tr.Status(ctx)
	if !reflect.DeepEqual(before, after) || calls.Load() != 0 {
		t.Fatal("paused worker changed pending jobs or made an external request")
	}
	// The mode is persisted beside the encrypted key; restarting keeps the pause.
	tr.allowLoopbackHTTP = false
	stored, err := s.GetValue(ctx, translationConfigKey)
	if err != nil || !strings.Contains(stored, `"execution_mode":"paused"`) || strings.Contains(stored, activeConfig.APIKey) {
		t.Fatal("paused mode or encrypted key persistence failed")
	}
	// Use a valid production endpoint for restart, without resolving or calling it.
	paused.Endpoint = "https://translation.example/v1"
	paused.APIKey = "restart-test-key"
	if err := tr.Configure(ctx, paused); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewTranslator(s, cfg)
	if err != nil || reloaded.secretConfiguration().ExecutionMode != translationModePaused || reloaded.secretConfiguration().APIKey != "restart-test-key" {
		t.Fatal("restart did not retain the pause and encrypted key")
	}
	if err := tr.Configure(ctx, TranslationConfig{ExecutionMode: "unsupported"}); err == nil {
		t.Fatal("unknown execution mode was accepted")
	}
}

func TestTranslationPauseCancelsInFlightAndPreservesDrafts(t *testing.T) {
	s, tr, _ := testTranslator(t)
	p := testTranslationPost(t, s, "<p>正文</p>")
	started, cancelled := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		calls.Add(1)
		close(started)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(5 * time.Second):
			t.Error("pausing did not cancel the in-flight request")
		}
	})
	if err := tr.Queue(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	job, err := tr.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { tr.process(context.Background(), job, tr.secretConfiguration()); close(done) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("local request did not start")
	}
	c, _ := tr.Configuration(context.Background())
	c.ExecutionMode = translationModePaused
	if err := tr.Configure(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	for _, wait := range []chan struct{}{cancelled, done} {
		select {
		case <-wait:
		case <-time.After(5 * time.Second):
			t.Fatal("paused request did not finish cancellation")
		}
	}
	stored, err := s.GetPost(context.Background(), p.ID)
	if err != nil || len(stored.Translations) != 0 || calls.Load() != 1 || findTranslationJob(t, tr, job.ID).Status != "retry" {
		t.Fatal("paused request stored content, retried, or lost its pending state")
	}
	noTranslationReady(t, tr)
}

func TestTranslationManualModeRunsOnlyExplicitJobsAndPersists(t *testing.T) {
	s, tr, appConfig := testTranslator(t)
	ctx := context.Background()
	p := testTranslationPost(t, s, "<p>正文</p>")
	var calls atomic.Int32
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		calls.Add(1)
		respondTranslation(w, slots)
	})
	manual, _ := tr.Configuration(ctx)
	manual.ExecutionMode = translationModeManual
	if err := tr.Configure(ctx, manual); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePost(ctx, p.ID, func(p *Post) error { p.ContentHTML = "<p>正文介绍</p>"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := tr.QueueAutomatic(ctx, p); err != nil {
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	tr.Run(workerCtx)
	cancel()
	jobs, err := tr.Status(ctx)
	if err != nil || len(jobs) != 0 || calls.Load() != 0 {
		t.Fatal("manual mode saved or processed automatic work without a click")
	}
	if err := tr.Queue(ctx, p, false); err != nil {
		t.Fatal(err)
	}
	before, _ := tr.Status(ctx)
	if len(before) != len(translationLanguages()) {
		t.Fatal("manual click did not enqueue all ten languages")
	}
	// Saving the same mode, including an old client omitting it, must preserve
	// explicitly requested jobs rather than treating it as a new mode switch.
	legacySave := manual
	legacySave.ExecutionMode = ""
	if err := tr.Configure(ctx, legacySave); err != nil {
		t.Fatal(err)
	}
	after, _ := tr.Status(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("same-mode configuration save cancelled explicitly requested jobs")
	}
	for range translationLanguages() {
		processNextTranslation(t, tr)
	}
	stored, err := s.GetPost(ctx, p.ID)
	if err != nil || len(stored.Translations) != len(translationLanguages()) || calls.Load() != int32(len(translationLanguages())) {
		t.Fatal("manual click did not run all requested translations")
	}
	for _, job := range before {
		if findTranslationJob(t, tr, job.ID).Status != "completed" {
			t.Fatal("manual job did not finish")
		}
	}
	// Persist a structurally valid endpoint for restart without resolving or
	// contacting it. The test key is retained and remains masked on read.
	manual.Endpoint, manual.APIKey = "https://translation.example/v1", "test-not-a-real-key"
	if err := tr.Configure(ctx, manual); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewTranslator(s, appConfig)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := reloaded.Configuration(ctx)
	if err != nil || configuration.ExecutionMode != translationModeManual || !configuration.HasAPIKey || configuration.APIKey != "" {
		t.Fatal("manual mode or secret masking did not survive restart")
	}
	if err := reloaded.QueueAutomatic(ctx, p); err != nil {
		t.Fatal(err)
	}
	noTranslationReady(t, reloaded)
}

func TestTranslationManualTransitionCancelsRunningAndQueuedJobs(t *testing.T) {
	s, tr, _ := testTranslator(t)
	ctx := context.Background()
	p := testTranslationPost(t, s, "<p>正文</p>")
	started, cancelled := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		if calls.Add(1) != 1 {
			respondTranslation(w, slots)
			return
		}
		close(started)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(5 * time.Second):
			t.Error("manual transition did not cancel the old request")
		}
	})
	if err := tr.Queue(ctx, p, false); err != nil {
		t.Fatal(err)
	}
	job, err := tr.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { tr.process(ctx, job, tr.secretConfiguration()); close(done) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("local automatic request did not start")
	}
	manual, _ := tr.Configuration(ctx)
	manual.ExecutionMode = translationModeManual
	if err := tr.Configure(ctx, manual); err != nil {
		t.Fatal(err)
	}
	for _, wait := range []chan struct{}{cancelled, done} {
		select {
		case <-wait:
		case <-time.After(5 * time.Second):
			t.Fatal("manual transition did not finish cancellation")
		}
	}
	jobs, err := tr.Status(ctx)
	if err != nil || len(jobs) != len(translationLanguages()) {
		t.Fatal("manual transition lost queue history")
	}
	for _, queued := range jobs {
		if queued.Status != "cancelled" {
			t.Fatal("old automatic work was restored as retryable")
		}
	}
	workerCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	tr.Run(workerCtx)
	cancel()
	stored, err := s.GetPost(ctx, p.ID)
	if err != nil || len(stored.Translations) != 0 || calls.Load() != 1 {
		t.Fatal("manual transition continued old work without a new click")
	}
	if err := tr.Queue(ctx, p, false); err != nil {
		t.Fatal(err)
	}
	if err := tr.Configure(ctx, manual); err != nil {
		t.Fatal(err)
	}
	for range translationLanguages() {
		processNextTranslation(t, tr)
	}
	if calls.Load() != int32(1+len(translationLanguages())) {
		t.Fatal("new manual click could not resume translation")
	}
}

func TestTranslationPausedToManualCancelsExistingJobsAndProtectsHumanDrafts(t *testing.T) {
	s, tr, _ := testTranslator(t)
	ctx := context.Background()
	p := testTranslationPost(t, s, "<p>正文</p>")
	if err := tr.Queue(ctx, p, false); err != nil {
		t.Fatal(err)
	}
	c, _ := tr.Configuration(ctx)
	c.ExecutionMode = translationModePaused
	if err := tr.Configure(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.ExecutionMode = translationModeManual
	if err := tr.Configure(ctx, c); err != nil {
		t.Fatal(err)
	}
	jobs, err := tr.Status(ctx)
	if err != nil || len(jobs) != len(translationLanguages()) {
		t.Fatal("paused-to-manual transition lost jobs")
	}
	for _, job := range jobs {
		if job.Status != "cancelled" {
			t.Fatal("paused-to-manual transition resumed old work without a click")
		}
	}
	if err := s.UpdatePost(ctx, p.ID, func(p *Post) error {
		for _, language := range translationLanguages() {
			p.Translations[language] = Translation{Title: "Reviewed " + language, ContentHTML: "<p>Human draft</p>", SourceHash: SourceHash(*p), Status: "publish"}
		}
		p.ContentHTML = "<p>后续正文</p>"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetPost(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Queue(ctx, p, false); err != nil {
		t.Fatal(err)
	}
	noTranslationReady(t, tr)
	after, err := s.GetPost(ctx, p.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("ordinary manual click changed human-reviewed drafts")
	}
}

func TestTranslationAllManualLanguagesSurviveLaterSourceChanges(t *testing.T) {
	s, tr, _ := testTranslator(t)
	p := testTranslationPost(t, s, "<p>正文</p>")
	if err := s.UpdatePost(context.Background(), p.ID, func(p *Post) error {
		for _, language := range translationLanguages() {
			p.Translations[language] = Translation{Title: "GPT approved " + language, ContentHTML: "<p>Human-reviewed content</p>", Status: "publish", SourceHash: SourceHash(*p)}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePost(context.Background(), p.ID, func(p *Post) error { p.ContentHTML = "<p>后续正文修改</p>"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := tr.QueueAutomatic(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	noTranslationReady(t, tr)
	jobs, err := tr.Status(context.Background())
	if err != nil || len(jobs) != 0 {
		t.Fatal("manual GPT translations were queued for automatic replacement")
	}
	stored, err := s.GetPost(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, language := range translationLanguages() {
		if stored.Translations[language].Title != "GPT approved "+language || stored.Translations[language].MachineHash != "" {
			t.Fatal("manual GPT draft changed or was marked as machine generated")
		}
	}
}

func TestTranslationProviderRequestAndJSONResponse(t *testing.T) {
	for _, test := range []struct{ provider, model, tokenField string }{
		{"openai", "openai-test-model", "max_completion_tokens"},
		{"deepseek", "", "max_tokens"},
		{"deepseek", "deepseek-v4-pro", "max_tokens"},
		{"compatible", "custom-test-model", "max_completion_tokens"},
		{"", "legacy-test-model", "max_completion_tokens"},
	} {
		t.Run(test.provider+test.model, func(t *testing.T) {
			_, tr, _ := testTranslator(t)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer provider-test-key" {
					t.Error("invalid provider request destination or authorization")
					w.WriteHeader(400)
					return
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				wantModel := test.model
				if wantModel == "" {
					wantModel = translationDeepSeekModel
				}
				var model string
				_ = json.Unmarshal(body["model"], &model)
				if model != wantModel || string(body[test.tokenField]) != "8192" || string(body["response_format"]) != `{"type":"json_object"}` {
					t.Error("provider model, output limit or JSON format is incorrect")
				}
				if test.provider == "deepseek" {
					if string(body["thinking"]) != `{"type":"disabled"}` || len(body["max_completion_tokens"]) != 0 {
						t.Error("DeepSeek received unsupported output limits or enabled thinking")
					}
				} else if len(body["thinking"]) != 0 || len(body["max_tokens"]) != 0 {
					t.Error("DeepSeek-only fields were sent to another provider")
				}
				var messages []struct{ Role, Content string }
				_ = json.Unmarshal(body["messages"], &messages)
				if len(messages) != 2 || messages[0].Role != "system" || !strings.Contains(messages[0].Content, "JSON") || messages[1].Role != "user" {
					t.Error("missing structured translation instructions")
					return
				}
				var input struct {
					Target string            `json:"target_language"`
					Slots  []translationSlot `json:"slots"`
				}
				if json.Unmarshal([]byte(messages[1].Content), &input) != nil || input.Target != "English" || len(input.Slots) != 1 {
					t.Error("translation input was not preserved")
					return
				}
				respondTranslation(w, input.Slots)
			}))
			defer server.Close()
			tr.allowLoopbackHTTP = true
			if err := tr.Configure(context.Background(), TranslationConfig{Provider: test.provider, Endpoint: server.URL + "/v1/chat/completions/", Model: test.model, APIKey: "provider-test-key"}); err != nil {
				t.Fatal(err)
			}
			result, err := tr.translateBatch(context.Background(), []translationSlot{{ID: "title", Text: "你好"}}, "en", tr.secretConfiguration())
			if err != nil || result["title"] != "Hello" || calls != 1 {
				t.Fatalf("provider translation failed: calls=%d, error=%v", calls, err)
			}
		})
	}
}

func TestTranslationQueueDeduplicatesAndRecoversRunning(t *testing.T) {
	s, tr, cfg := testTranslator(t)
	ctx := context.Background()
	p := testTranslationPost(t, s, "<p>正文</p>")
	if err := tr.Queue(ctx, p, false); err != nil {
		t.Fatal(err)
	}
	if err := tr.Queue(ctx, p, false); err != nil {
		t.Fatal(err)
	}
	jobs, err := tr.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != len(translationLanguages()) {
		t.Fatalf("got %d jobs, want all translation languages", len(jobs))
	}
	languages := map[string]bool{}
	for _, j := range jobs {
		languages[j.Language] = true
	}
	for _, language := range translationLanguages() {
		if !languages[language] {
			t.Fatalf("queue omitted language %s", language)
		}
	}
	claimed, err := tr.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewTranslator(s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	job := findTranslationJob(t, reloaded, claimed.ID)
	if job.Status != "retry" || job.Attempts != 1 {
		t.Fatalf("running job was not recovered: %+v", job)
	}
	p.Status = "draft"
	if err := s.SavePost(ctx, &p); err != nil {
		t.Fatal(err)
	}
	if err := tr.Queue(ctx, p, true); err != nil {
		t.Fatal(err)
	}
	jobs, err = tr.Status(ctx)
	if err != nil || len(jobs) != len(translationLanguages()) {
		t.Fatal("draft queued translation")
	}
}

func TestTranslationPreservesHTMLMediaLinksNumbersAndCode(t *testing.T) {
	s, tr, _ := testTranslator(t)
	content := `<!-- 中文评论保留 --><p class='keep' data-src="原属性">你好甘，访问 <a href='https://ganwen.nl/?p=42'>WordPress</a>，价格 2026.50；Vic Wong。</p><img src='/wp-content/uploads/2026/a.jpg' alt='中文图片' loading='lazy'><pre><code>中文 code &lt;tag&gt; 123</code></pre><script>const x = '中文';</script><style>.中文 { color: red; }</style><p>图片 &amp; 正文</p>`
	p := testTranslationPost(t, s, content)
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) { respondTranslation(w, slots) })
	if err := tr.Queue(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	j := processNextTranslation(t, tr)
	stored, err := s.GetPost(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	translated := stored.Translations[j.Language]
	if translated.Status != "review" || translated.SourceHash != SourceHash(p) {
		t.Fatalf("unexpected translation status: %+v", translated)
	}
	if stored.ContentHTML != content {
		t.Fatal("source HTML changed")
	}
	for _, want := range []string{`<!-- 中文评论保留 -->`, `<p class='keep' data-src="原属性">`, `href='https://ganwen.nl/?p=42'`, `<img src='/wp-content/uploads/2026/a.jpg' alt='中文图片' loading='lazy'>`, `<pre><code>中文 code &lt;tag&gt; 123</code></pre>`, `<script>const x = '中文';</script>`, `<style>.中文 { color: red; }</style>`, `2026.50`, `Vic Wong`, `甘`, `&amp;`} {
		if !strings.Contains(translated.ContentHTML, want) {
			t.Errorf("translation changed protected content: %s", want)
		}
	}
	if !strings.Contains(translated.Title, "Hello甘") || !strings.Contains(translated.ContentHTML, "Hello") || !strings.Contains(translated.ExcerptHTML, "Body") || translated.SEO.Title != "Hello" {
		t.Fatalf("text fields were not translated: %+v", translated)
	}
	if got := findTranslationJob(t, tr, j.ID); got.Status != "completed" {
		t.Fatalf("job did not complete: %+v", got)
	}
}

func TestTranslationAutoPublishIsExplicitAndExistingReviewIsPreserved(t *testing.T) {
	s, tr, _ := testTranslator(t)
	p := testTranslationPost(t, s, "<p>正文</p>")
	var calls atomic.Int32
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		calls.Add(1)
		respondTranslation(w, slots)
	})
	c := tr.secretConfiguration()
	c.AutoPublish = true
	if err := tr.Configure(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := tr.Queue(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	j := processNextTranslation(t, tr)
	stored, err := s.GetPost(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Translations[j.Language].Status != "publish" {
		t.Fatal("explicit auto publish setting was ignored")
	}
	jobs, err := tr.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var manualJobID int64
	for _, job := range jobs {
		if job.Language == "en" {
			manualJobID = job.ID
		}
	}
	if err := s.UpdatePost(context.Background(), p.ID, func(p *Post) error {
		p.Translations["en"] = Translation{Title: "人工译文", Status: "review", SourceHash: SourceHash(*p)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for range len(translationLanguages()) - 2 {
		job := processNextTranslation(t, tr)
		if job.Language == "en" || findTranslationJob(t, tr, job.ID).Status != "completed" {
			t.Fatal("non-manual translation was not processed")
		}
	}
	if got := findTranslationJob(t, tr, manualJobID); got.Status != "cancelled" || got.Attempts != 0 {
		t.Fatal("obsolete manual-review job must be cancelled before a service attempt")
	}
	if err := tr.Queue(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.claim(context.Background()); err == nil {
		t.Fatal("normal queue replaced reviewed or published translations")
	}
	if calls.Load() != int32(len(translationLanguages())-1) {
		t.Fatal("manually reviewed language triggered an unnecessary request")
	}
	stored, err = s.GetPost(context.Background(), p.ID)
	if err != nil || stored.Translations["en"].Title != "人工译文" || stored.Translations["en"].Status != "review" {
		t.Fatal("manual review was changed")
	}
}

func TestTranslationSourceChangeCannotBeOverwritten(t *testing.T) {
	s, tr, _ := testTranslator(t)
	p := testTranslationPost(t, s, "<p>正文</p>")
	started := make(chan struct{})
	release := make(chan struct{})
	var once atomic.Bool
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		if once.CompareAndSwap(false, true) {
			close(started)
			<-release
		}
		respondTranslation(w, slots)
	})
	if err := tr.Queue(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	j, err := tr.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { tr.process(context.Background(), j, tr.secretConfiguration()); close(done) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("local translation did not start")
	}
	if err := s.UpdatePost(context.Background(), p.ID, func(p *Post) error { p.ContentHTML = "<p>新的正文</p>"; return nil }); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("local translation did not finish")
	}
	stored, err := s.GetPost(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Translations) != 0 {
		t.Fatal("old translation overwrote newer source")
	}
	if got := findTranslationJob(t, tr, j.ID); got.Status != "cancelled" {
		t.Fatalf("stale job not cancelled: %+v", got)
	}
}

func TestTranslationManualEditDuringForceIsPreserved(t *testing.T) {
	s, tr, _ := testTranslator(t)
	p := testTranslationPost(t, s, "<p>正文</p>")
	if err := s.UpdatePost(context.Background(), p.ID, func(p *Post) error {
		p.Translations[translationLanguages()[0]] = Translation{Title: "original manual review", Status: "review", SourceHash: SourceHash(*p)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var once atomic.Bool
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		if once.CompareAndSwap(false, true) {
			close(started)
			<-release
		}
		respondTranslation(w, slots)
	})
	if err := tr.Queue(context.Background(), p, true); err != nil {
		t.Fatal(err)
	}
	j, err := tr.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { tr.process(context.Background(), j, tr.secretConfiguration()); close(done) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("local translation did not start")
	}
	if err := s.UpdatePost(context.Background(), p.ID, func(p *Post) error {
		v := p.Translations[translationLanguages()[0]]
		v.Title = "new administrator edit"
		p.Translations[translationLanguages()[0]] = v
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("translation did not finish")
	}
	stored, err := s.GetPost(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Translations[translationLanguages()[0]].Title != "new administrator edit" {
		t.Fatal("force overwrote edit made while translation was running")
	}
	if got := findTranslationJob(t, tr, j.ID); got.Status != "cancelled" {
		t.Fatalf("manual conflict not cancelled: %+v", got)
	}
}

func TestTranslationFailureNeverStoresPartialTranslationOrServiceSecrets(t *testing.T) {
	s, tr, _ := testTranslator(t)
	p := testTranslationPost(t, s, "<p>"+strings.Repeat("正文。", 3000)+"</p>")
	var calls atomic.Int32
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		if calls.Add(1) == 1 {
			respondTranslation(w, slots)
			return
		}
		w.WriteHeader(502)
		fmt.Fprint(w, "provider says test-not-a-real-key secret response")
	})
	if err := tr.Queue(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	j := processNextTranslation(t, tr)
	stored, err := s.GetPost(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Translations) != 0 {
		t.Fatal("partial translation was saved")
	}
	got := findTranslationJob(t, tr, j.ID)
	if got.Status != "retry" || got.Attempts != 1 {
		t.Fatalf("failed task was not retried: %+v", got)
	}
	if strings.Contains(got.LastError, "secret") || strings.Contains(got.LastError, "test-not-a-real-key") {
		t.Fatal("service error leaked secret")
	}
	if calls.Load() != 2 {
		t.Fatalf("unexpected batching: %d requests", calls.Load())
	}
}

func TestTranslationRejectsMalformedSlotsAndLostProtectedValues(t *testing.T) {
	for _, mode := range []string{"missing", "extra", "duplicate", "number"} {
		t.Run(mode, func(t *testing.T) {
			s, tr, _ := testTranslator(t)
			p := testTranslationPost(t, s, "<p>价格 42</p>")
			testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
				out := append([]translationSlot(nil), slots...)
				switch mode {
				case "missing":
					out = out[:len(out)-1]
				case "extra":
					out = append(out, translationSlot{ID: "unrequested", Text: "extra"})
				case "duplicate":
					out[len(out)-1] = out[0]
				case "number":
					for i := range out {
						out[i].Text = strings.ReplaceAll(out[i].Text, "⟪GW_KEEP_0⟫", "changed")
					}
				}
				b, _ := json.Marshal(map[string]any{"slots": out})
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(b)}, "finish_reason": "stop"}}})
			})
			if err := tr.Queue(context.Background(), p, false); err != nil {
				t.Fatal(err)
			}
			j := processNextTranslation(t, tr)
			got := findTranslationJob(t, tr, j.ID)
			if got.Status != "retry" {
				t.Fatalf("invalid response completed: %+v", got)
			}
			stored, err := s.GetPost(context.Background(), p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(stored.Translations) != 0 {
				t.Fatal("invalid result was stored")
			}
		})
	}
}

func TestTranslationProtectsLiteralPlaceholderSource(t *testing.T) {
	doc, err := newTranslationDocument("中文 ⟪GW_KEEP_0⟫ 甘 42", true, "title", "甘")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, s := range doc.slots {
		values[s.ID] = strings.ReplaceAll(s.Text, "中文", "Chinese")
	}
	got, err := doc.render(values)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Chinese ⟪GW_KEEP_0⟫ 甘 42" {
		t.Fatalf("protected source changed: %s", got)
	}
}

func TestSpanishWorkerUsesSpanishTargetAndPreservesHTML(t *testing.T) {
	s, tr, _ := testTranslator(t)
	ctx := context.Background()
	p := testTranslationPost(t, s, `<p>正文 <img src="/wp-content/uploads/a.jpg"></p>`)
	var calls atomic.Int32
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		calls.Add(1)
		if r.Header.Get("X-Test-Translation-Target") != "Spanish" {
			t.Error("worker sent a non-Spanish target")
		}
		result := make([]translationSlot, len(slots))
		replace := strings.NewReplacer("你好", "Hola", "正文", "Contenido", "摘要", " resumen", "介绍", " descripción")
		for i, slot := range slots {
			result[i] = translationSlot{ID: slot.ID, Text: replace.Replace(slot.Text)}
		}
		raw, _ := json.Marshal(map[string]any{"slots": result})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(raw)}, "finish_reason": "stop"}}})
	})
	if err := tr.Queue(ctx, p, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE translation_jobs SET status='completed' WHERE language<>'es'"); err != nil {
		t.Fatal(err)
	}
	job := processNextTranslation(t, tr)
	if job.Language != "es" || findTranslationJob(t, tr, job.ID).Status != "completed" {
		t.Fatal("Spanish job was not completed")
	}
	stored, err := s.GetPost(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	spanish := stored.Translations["es"]
	if spanish.Status != "review" || spanish.SourceHash != SourceHash(stored) || spanish.MachineHash != TranslationContentHash(spanish) || !strings.Contains(spanish.Title, "Hola") || !strings.Contains(spanish.ContentHTML, `src="/wp-content/uploads/a.jpg"`) {
		t.Fatal("Spanish worker lost translated text, review state or media/source integrity")
	}
	if _, err = tr.translate(ctx, p, "de", tr.secretConfiguration()); err == nil {
		t.Fatal("unsupported target accepted")
	}
	if calls.Load() != 1 {
		t.Fatal("unsupported language made a service request")
	}
}

func TestTranslationRefreshesUneditedMachineContentAndProtectsManualChanges(t *testing.T) {
	s, tr, _ := testTranslator(t)
	p := testTranslationPost(t, s, "<p>正文</p>")
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) { respondTranslation(w, slots) })
	if err := tr.Queue(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	for range translationLanguages() {
		processNextTranslation(t, tr)
	}
	if err := s.UpdatePost(context.Background(), p.ID, func(p *Post) error { p.Title = "你好中文"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := tr.Queue(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	j := processNextTranslation(t, tr)
	for range len(translationLanguages()) - 1 {
		processNextTranslation(t, tr)
	}
	stored, err := s.GetPost(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Translations[j.Language].SourceHash != SourceHash(stored) || stored.Translations[j.Language].Title != "HelloChinese" {
		t.Fatal("machine translation was not refreshed after source changed")
	}
	if err := s.UpdatePost(context.Background(), p.ID, func(p *Post) error {
		v := p.Translations["en"]
		v.Title = "Manually improved"
		p.Translations["en"] = v
		p.Title = "你好中文正文"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := tr.Queue(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	j, err = tr.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if j.Language != "nl" {
		t.Fatal("manual English translation was queued for overwrite")
	}
	tr.process(context.Background(), j, tr.secretConfiguration())
	for range len(translationLanguages()) - 2 {
		job := processNextTranslation(t, tr)
		if job.Language == "en" {
			t.Fatal("manual English translation was queued for overwrite")
		}
	}
	if _, err := tr.claim(context.Background()); err == nil {
		t.Fatal("manual English translation was not protected")
	}
}

func TestTranslationEndpointRejectsPrivateDNSAndRebinding(t *testing.T) {
	_, tr, _ := testTranslator(t)
	for _, endpoint := range []string{"https://127.0.0.1/v1", "https://192.168.8.1/v1", "https://169.254.169.254/v1", "https://100.100.100.200/v1", "https://0.1.2.3/v1", "https://[::1]/v1", "https://localhost/v1"} {
		if err := tr.Configure(context.Background(), TranslationConfig{Endpoint: endpoint, Model: "test", APIKey: "test-key"}); err == nil {
			t.Fatalf("accepted private endpoint %s", endpoint)
		}
	}
	tr.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("192.168.8.1")}}, nil
	}
	if err := tr.Configure(context.Background(), TranslationConfig{Endpoint: "https://rebind.example/v1", Model: "test", APIKey: "test-key"}); err == nil {
		t.Fatal("accepted hostname resolving to private address")
	}
	tr.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	if err := tr.Configure(context.Background(), TranslationConfig{Endpoint: "https://rebind.example/v1", Model: "test", APIKey: "test-key"}); err != nil {
		t.Fatal(err)
	}
	tr.lookupIP = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	if _, err := tr.secureDialContext(context.Background(), "tcp", "rebind.example:443"); err == nil {
		t.Fatal("dial accepted private DNS rebinding")
	}
}

func TestTranslationUnconfiguredWorkerLeavesQueueWithoutCallingService(t *testing.T) {
	s, tr, _ := testTranslator(t)
	p := testTranslationPost(t, s, "<p>正文</p>")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	tr.allowLoopbackHTTP = true
	if err := tr.Configure(context.Background(), TranslationConfig{Endpoint: server.URL + "/v1", Model: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := tr.Queue(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	tr.Run(ctx)
	if calls.Load() != 0 {
		t.Fatal("worker called a service without an API key")
	}
	jobs, err := tr.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.Status != "queued" || j.Attempts != 0 {
			t.Fatal("unconfigured worker consumed a queued task")
		}
	}
}

func TestTranslationStopsRetryingAtLimit(t *testing.T) {
	s, tr, _ := testTranslator(t)
	p := testTranslationPost(t, s, "<p>正文</p>")
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) { w.WriteHeader(503) })
	if err := tr.Queue(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	var id int64
	for attempt := 1; attempt <= translationMaxAttempts; attempt++ {
		j, err := tr.claim(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		id = j.ID
		tr.process(context.Background(), j, tr.secretConfiguration())
		if _, err := s.db.Exec(`UPDATE translation_jobs SET next_attempt_at=? WHERE id=?`, translationNow(), id); err != nil {
			t.Fatal(err)
		}
		// Restrict the other language so this test retries the same persisted job.
		if _, err := s.db.Exec(`UPDATE translation_jobs SET next_attempt_at='9999-01-01T00:00:00.000000000Z' WHERE id<>?`, id); err != nil {
			t.Fatal(err)
		}
	}
	got := findTranslationJob(t, tr, id)
	if got.Status != "failed" || got.Attempts != translationMaxAttempts {
		t.Fatalf("retry limit did not stop the job: %+v", got)
	}
}

func makeAutomaticJobsDue(t *testing.T, tr *Translator, hash string) {
	t.Helper()
	if _, err := tr.store.db.Exec(`UPDATE translation_jobs SET next_attempt_at=? WHERE source_hash=? AND status IN ('queued','retry')`, translationNow(), hash); err != nil {
		t.Fatal(err)
	}
}

func noTranslationReady(t *testing.T, tr *Translator) {
	t.Helper()
	if _, err := tr.claim(context.Background()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unexpected ready translation job: %v", err)
	}
}

func TestTranslationAutomaticSavesCoalesceToLatestLanguageRequests(t *testing.T) {
	s, tr, _ := testTranslator(t)
	p := testTranslationPost(t, s, "<p>第一版正文</p>")
	counts := make(map[string]*atomic.Int32)
	for _, language := range translationLanguages() {
		counts[translationTargetName(language)] = new(atomic.Int32)
	}
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		target := r.Header.Get("X-Test-Translation-Target")
		count, ok := counts[target]
		if !ok {
			t.Errorf("unknown translation target %q", target)
			w.WriteHeader(400)
			return
		}
		count.Add(1)
		latest := false
		for _, slot := range slots {
			latest = latest || strings.Contains(slot.Text, "最终版正文")
		}
		if !latest {
			t.Error("service received a superseded source version")
		}
		respondTranslation(w, slots)
	})
	for _, content := range []string{"第一版正文", "第二版正文", "最终版正文"} {
		if err := s.UpdatePost(context.Background(), p.ID, func(p *Post) error { p.ContentHTML = "<p>" + content + "</p>"; return nil }); err != nil {
			t.Fatal(err)
		}
		if err := tr.QueueAutomatic(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		noTranslationReady(t, tr)
	}
	current, err := s.GetPost(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := tr.Status(context.Background())
	if err != nil || len(jobs) != len(translationLanguages()) {
		t.Fatal("effective queue must show all current language jobs")
	}
	for _, j := range jobs {
		due, err := time.Parse(translationTimeFormat, j.NextAttemptAt)
		if err != nil || time.Until(due) < 28*time.Second || j.SourceHash != SourceHash(current) || j.Attempts != 0 || j.Status != "queued" {
			t.Fatal("automatic job was not held for the latest source quiet period")
		}
	}
	if err := tr.QueueAutomatic(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	again, err := tr.Status(context.Background())
	if err != nil || len(again) != len(jobs) {
		t.Fatal("unchanged source altered effective queue")
	}
	for i := range jobs {
		if jobs[i] != again[i] {
			t.Fatal("unchanged source postponed or reset its existing task")
		}
	}
	var obsolete int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM translation_jobs WHERE status='cancelled' AND attempts=0`).Scan(&obsolete); err != nil || obsolete != 2*len(translationLanguages()) {
		t.Fatal("superseded versions must stay in history without service attempts")
	}
	makeAutomaticJobsDue(t, tr, SourceHash(current))
	for range translationLanguages() {
		processNextTranslation(t, tr)
	}
	if err := tr.QueueAutomatic(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	noTranslationReady(t, tr)
	for i := range counts {
		if counts[i].Load() != 1 {
			t.Fatalf("latest source language %s had %d actual requests", i, counts[i].Load())
		}
	}
	var history int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM translation_jobs`).Scan(&history); err != nil || history != 3*len(translationLanguages()) {
		t.Fatal("queue history was deleted or duplicate current jobs were inserted")
	}
	stored, err := s.GetPost(context.Background(), p.ID)
	if err != nil || len(stored.Translations) != len(translationLanguages()) {
		t.Fatal("latest language results missing")
	}
	for _, translated := range stored.Translations {
		if translated.SourceHash != SourceHash(current) {
			t.Fatal("superseded source translation was saved")
		}
	}
}

func TestTranslationSourceReversionReusesCurrentJobsAndRetainsHistory(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(fmt.Sprint("completed=", completed), func(t *testing.T) {
			s, tr, _ := testTranslator(t)
			p := testTranslationPost(t, s, "<p>第一版正文</p>")
			original := p.ContentHTML
			originalHash := SourceHash(p)
			var calls atomic.Int32
			testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
				calls.Add(1)
				respondTranslation(w, slots)
			})
			if err := tr.QueueAutomatic(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			first, _ := tr.Status(context.Background())
			if completed {
				makeAutomaticJobsDue(t, tr, originalHash)
				for range translationLanguages() {
					processNextTranslation(t, tr)
				}
			}
			if err := s.UpdatePost(context.Background(), p.ID, func(p *Post) error { p.ContentHTML = "<p>第二版正文</p>"; return nil }); err != nil {
				t.Fatal(err)
			}
			if err := tr.QueueAutomatic(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			if completed {
				current, _ := s.GetPost(context.Background(), p.ID)
				makeAutomaticJobsDue(t, tr, SourceHash(current))
				for range translationLanguages() {
					processNextTranslation(t, tr)
				}
			}
			if err := s.UpdatePost(context.Background(), p.ID, func(p *Post) error { p.ContentHTML = original; return nil }); err != nil {
				t.Fatal(err)
			}
			if err := tr.QueueAutomatic(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			current, err := tr.Status(context.Background())
			if err != nil || len(current) != len(translationLanguages()) {
				t.Fatal("source reversion did not show all current jobs")
			}
			for i, j := range current {
				if j.ID != first[i].ID || j.SourceHash != originalHash || j.Status != "queued" || j.Attempts != 0 {
					t.Fatal("newer intermediate IDs masked the reverted source job")
				}
			}
			noTranslationReady(t, tr)
			makeAutomaticJobsDue(t, tr, originalHash)
			for range translationLanguages() {
				processNextTranslation(t, tr)
			}
			want := int32(len(translationLanguages()))
			if completed {
				want *= 3
			}
			if calls.Load() != want {
				t.Fatalf("source reversion generated %d requests, want %d", calls.Load(), want)
			}
			var history int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM translation_jobs`).Scan(&history); err != nil || history != 2*len(translationLanguages()) {
				t.Fatal("source reversion deleted history or inserted duplicate hash jobs")
			}
			current, err = tr.Status(context.Background())
			if err != nil || len(current) != len(translationLanguages()) {
				t.Fatal("current completed jobs missing")
			}
			for _, j := range current {
				if j.SourceHash != originalHash || j.Status != "completed" {
					t.Fatal("completed reverted source projection picked intermediate history")
				}
			}
		})
	}
}

func TestTranslationManualForcePromotesPendingAndDoesNotRestartActiveJob(t *testing.T) {
	s, tr, _ := testTranslator(t)
	p := testTranslationPost(t, s, "<p>正文</p>")
	started, release := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	var calls atomic.Int32
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		calls.Add(1)
		if once.CompareAndSwap(false, true) {
			close(started)
			<-release
		}
		respondTranslation(w, slots)
	})
	if err := tr.QueueAutomatic(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	noTranslationReady(t, tr)
	before, _ := tr.Status(context.Background())
	if err := tr.Queue(context.Background(), p, true); err != nil {
		t.Fatal(err)
	}
	j, err := tr.claim(context.Background())
	if err != nil {
		t.Fatal("explicit manual request did not immediately promote automatic work")
	}
	done := make(chan struct{})
	go func() { tr.process(context.Background(), j, tr.secretConfiguration()); close(done) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("manual local request did not start")
	}
	for range 5 {
		if err := tr.Queue(context.Background(), p, true); err != nil {
			t.Fatal(err)
		}
	}
	if active := findTranslationJob(t, tr, j.ID); active.Status != "running" || active.Attempts != 1 {
		t.Fatal("repeated force changed the active job")
	}
	current, _ := tr.Status(context.Background())
	if len(current) != len(translationLanguages()) {
		t.Fatal("repeated force added duplicate current jobs")
	}
	for i := range current {
		if current[i].ID != before[i].ID {
			t.Fatal("force promotion replaced a unique source job")
		}
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("manual local request did not finish")
	}
	for range len(translationLanguages()) - 1 {
		processNextTranslation(t, tr)
	}
	noTranslationReady(t, tr)
	if calls.Load() != int32(len(translationLanguages())) {
		t.Fatal("repeated active force caused extra actual requests")
	}
}

func TestTranslationSupersededInFlightRequestCancelsAndStopsOldBatches(t *testing.T) {
	s, tr, _ := testTranslator(t)
	p := testTranslationPost(t, s, "<p>"+strings.Repeat("旧正文。", 3000)+"</p>")
	started, cancelled := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	var calls atomic.Int32
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		calls.Add(1)
		if once.CompareAndSwap(false, true) {
			close(started)
			select {
			case <-r.Context().Done():
				close(cancelled)
			case <-time.After(5 * time.Second):
				t.Error("superseded HTTP request was not cancelled")
			}
			return
		}
		respondTranslation(w, slots)
	})
	if err := tr.Queue(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	j, err := tr.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { tr.process(context.Background(), j, tr.secretConfiguration()); close(done) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("local request did not start")
	}
	if err := s.UpdatePost(context.Background(), p.ID, func(p *Post) error { p.ContentHTML = "<p>最新正文</p>"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := tr.QueueAutomatic(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("obsolete service request remained active")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("obsolete local processing did not finish")
	}
	if calls.Load() != 1 || findTranslationJob(t, tr, j.ID).Status != "cancelled" {
		t.Fatal("obsolete version continued into another service batch")
	}
	stored, err := s.GetPost(context.Background(), p.ID)
	if err != nil || len(stored.Translations) != 0 {
		t.Fatal("cancelled request stored a partial or obsolete translation")
	}
	noTranslationReady(t, tr)
	makeAutomaticJobsDue(t, tr, SourceHash(stored))
	for range translationLanguages() {
		processNextTranslation(t, tr)
	}
	if calls.Load() != int32(len(translationLanguages())+1) {
		t.Fatal("latest version did not receive exactly one request per language")
	}
	var noAttempt int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM translation_jobs WHERE status='cancelled' AND attempts=0`).Scan(&noAttempt); err != nil || noAttempt != len(translationLanguages())-1 {
		t.Fatal("obsolete not-yet-started languages consumed service attempts")
	}
}

func TestTranslationQuietJobsAndStaleHistoryRecoverAcrossRestart(t *testing.T) {
	s, tr, cfg := testTranslator(t)
	p := testTranslationPost(t, s, "<p>正文</p>")
	if err := tr.QueueAutomatic(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	before, _ := tr.Status(context.Background())
	reloaded, err := NewTranslator(s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := reloaded.Status(context.Background())
	for i := range before {
		if before[i] != current[i] {
			t.Fatal("restart changed the quiet-period job or its due date")
		}
	}
	noTranslationReady(t, reloaded)
	if _, err := s.db.Exec(`UPDATE translation_jobs SET status='running',attempts=1 WHERE id=?`, before[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePost(context.Background(), p.ID, func(p *Post) error { p.ContentHTML = "<p>新的正文</p>"; return nil }); err != nil {
		t.Fatal(err)
	}
	reloaded, err = NewTranslator(s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range before {
		if got := findTranslationJob(t, reloaded, j.ID); got.Status != "cancelled" {
			t.Fatal("restart retained an obsolete queued/recovered task")
		}
	}
	if err := reloaded.QueueAutomatic(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	testTranslationServer(t, reloaded, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		calls.Add(1)
		respondTranslation(w, slots)
	})
	stored, _ := s.GetPost(context.Background(), p.ID)
	makeAutomaticJobsDue(t, reloaded, SourceHash(stored))
	for range translationLanguages() {
		processNextTranslation(t, reloaded)
	}
	if calls.Load() != int32(len(translationLanguages())) {
		t.Fatal("restart recovery made requests for an obsolete source")
	}
}

func TestTranslationSourceReversionBetweenClaimAndProcessKeepsQuietPeriod(t *testing.T) {
	s, tr, _ := testTranslator(t)
	p := testTranslationPost(t, s, "<p>第一版正文</p>")
	original := p.ContentHTML
	var calls atomic.Int32
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		calls.Add(1)
		respondTranslation(w, slots)
	})
	if err := tr.Queue(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	old, err := tr.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"<p>第二版正文</p>", original} {
		if err := s.UpdatePost(context.Background(), p.ID, func(p *Post) error { p.ContentHTML = content; return nil }); err != nil {
			t.Fatal(err)
		}
		if err := tr.QueueAutomatic(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	tr.process(context.Background(), old, tr.secretConfiguration())
	if calls.Load() != 0 {
		t.Fatal("old claim bypassed the reverted source's quiet period")
	}
	if current := findTranslationJob(t, tr, old.ID); current.Status != "queued" || current.Attempts != 0 || current.NextAttemptAt == old.NextAttemptAt {
		t.Fatal("old processing changed the new generation of a reused row")
	}
	noTranslationReady(t, tr)
	makeAutomaticJobsDue(t, tr, SourceHash(p))
	latest, err := tr.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The old object has the same row ID and attempt count as the new claim.
	// Its finishing path must still be unable to overwrite the new execution.
	if latest.ID != old.ID || latest.Attempts != old.Attempts || latest.NextAttemptAt == old.NextAttemptAt {
		t.Fatal("regression fixture did not recreate an old-ID execution collision")
	}
	tr.process(context.Background(), old, tr.secretConfiguration())
	tr.finish(context.Background(), old, errTranslationStale)
	if current := findTranslationJob(t, tr, latest.ID); current.Status != "running" || current.NextAttemptAt != latest.NextAttemptAt || calls.Load() != 0 {
		t.Fatal("obsolete execution wrote over the current claimed task")
	}
	tr.process(context.Background(), latest, tr.secretConfiguration())
	for range len(translationLanguages()) - 1 {
		processNextTranslation(t, tr)
	}
	if calls.Load() != int32(len(translationLanguages())) {
		t.Fatal("reused source ID made duplicate service requests")
	}
}

func TestTranslationManualDraftIsProtectedAcrossSourceChanges(t *testing.T) {
	s, tr, _ := testTranslator(t)
	p := testTranslationPost(t, s, "<p>正文</p>")
	if err := s.UpdatePost(context.Background(), p.ID, func(p *Post) error {
		p.Translations = map[string]Translation{"en": {Title: "Manual draft", ContentHTML: "<p>Human text</p>", Status: "draft", SourceHash: SourceHash(*p)}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePost(context.Background(), p.ID, func(p *Post) error { p.ContentHTML = "<p>新的正文</p>"; return nil }); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		calls.Add(1)
		if r.Header.Get("X-Test-Translation-Target") == "English" {
			t.Error("manual draft was requested for overwrite")
		}
		respondTranslation(w, slots)
	})
	if err := tr.QueueAutomatic(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	current, _ := s.GetPost(context.Background(), p.ID)
	makeAutomaticJobsDue(t, tr, SourceHash(current))
	for range len(translationLanguages()) - 1 {
		processNextTranslation(t, tr)
	}
	if err := tr.Queue(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	noTranslationReady(t, tr)
	stored, err := s.GetPost(context.Background(), p.ID)
	if err != nil || stored.Translations["en"].Title != "Manual draft" || stored.Translations["en"].ContentHTML != "<p>Human text</p>" || stored.Translations["en"].Status != "draft" || calls.Load() != int32(len(translationLanguages())-1) {
		t.Fatal("manual draft content/state was overwritten or requested")
	}
}

func TestTranslationSourceGuardStopsNextBatchWithoutQueueNotification(t *testing.T) {
	s, tr, _ := testTranslator(t)
	p := testTranslationPost(t, s, "<p>"+strings.Repeat("旧正文。", 3000)+"</p>")
	var calls atomic.Int32
	testTranslationServer(t, tr, func(w http.ResponseWriter, r *http.Request, slots []translationSlot) {
		calls.Add(1)
		if err := s.UpdatePost(context.Background(), p.ID, func(p *Post) error { p.ContentHTML = "<p>新的正文</p>"; return nil }); err != nil {
			t.Error(err)
		}
		respondTranslation(w, slots)
	})
	if err := tr.Queue(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	j := processNextTranslation(t, tr)
	if calls.Load() != 1 || findTranslationJob(t, tr, j.ID).Status != "cancelled" {
		t.Fatal("source change between batches allowed obsolete later requests")
	}
	noTranslationReady(t, tr)
	var untouched int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM translation_jobs WHERE status='cancelled' AND attempts=0`).Scan(&untouched); err != nil || untouched != len(translationLanguages())-1 {
		t.Fatal("stale legacy queued languages were claimed as service attempts")
	}
	stored, err := s.GetPost(context.Background(), p.ID)
	if err != nil || len(stored.Translations) != 0 {
		t.Fatal("incomplete superseded translation was persisted")
	}
}

func TestTranslationAdminSaveUsesQuietPeriodAndMetadataDoesNotReschedule(t *testing.T) {
	s, tr, _ := testTranslator(t)
	p := testTranslationPost(t, s, "<p>正文</p>")
	admin := NewAdmin(&App{Store: s, Translator: tr})
	save := func(p Post) Post {
		t.Helper()
		body, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/admin/api/post", strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		admin.savePost(w, r, false)
		if w.Code != http.StatusOK {
			t.Fatalf("admin save returned %d", w.Code)
		}
		var response struct {
			Post Post `json:"post"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Post
	}
	p.ContentHTML = "<p>新的正文</p>"
	p = save(p)
	noTranslationReady(t, tr)
	before, err := tr.Status(context.Background())
	if err != nil || len(before) != len(translationLanguages()) {
		t.Fatal("published source change did not create all quiet-period jobs")
	}
	p.Sticky = !p.Sticky
	p = save(p)
	after, err := tr.Status(context.Background())
	if err != nil || len(after) != len(before) {
		t.Fatal("metadata save altered translation queue")
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatal("unchanged source metadata save postponed or replaced translation work")
		}
	}
	p.Status = "draft"
	save(p)
	noTranslationReady(t, tr)
	for _, j := range before {
		if current := findTranslationJob(t, tr, j.ID); current.Status != "cancelled" || current.Attempts != 0 {
			t.Fatal("unpublishing did not cancel automatic work before an API attempt")
		}
	}
}
