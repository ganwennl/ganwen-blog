package blog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

const contactNoticeMaxRunes = 4000

type ContactNoticeTranslation struct {
	Text       string `json:"text"`
	SourceHash string `json:"source_hash"`
}

func validContactNoticeText(text string) bool {
	return utf8.ValidString(text) && utf8.RuneCountInString(text) <= contactNoticeMaxRunes && strings.IndexFunc(text, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
	}) < 0
}

func contactNoticeSourceHash(text string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSpace(text))))
}

func publicContactNoticeText(settings SiteSettings, lang string) string {
	source := settings.ContactNoticeText
	if translated, ok := settings.ContactNoticeTranslations[lang]; isTranslationLanguage(lang) && ok && strings.TrimSpace(source) != "" && translated.SourceHash == contactNoticeSourceHash(source) && strings.TrimSpace(translated.Text) != "" && validContactNoticeText(translated.Text) {
		return translated.Text
	}
	return source
}

func validatedContactNoticeTranslations(input map[string]ContactNoticeTranslation, source string) (map[string]ContactNoticeTranslation, error) {
	if input == nil {
		return nil, nil
	}
	if len(input) > len(translationLanguages()) {
		return nil, errors.New("联系提示译文语言不正确")
	}
	out := make(map[string]ContactNoticeTranslation, len(input))
	for lang, translation := range input {
		if !isTranslationLanguage(lang) || !validContactNoticeText(translation.Text) {
			return nil, errors.New("联系提示译文请使用支持的语言和不超过 4000 字的纯文本")
		}
		if translation.SourceHash != "" {
			if hash, err := hex.DecodeString(translation.SourceHash); err != nil || len(hash) != sha256.Size {
				return nil, errors.New("联系提示译文版本不正确，请重新编辑或翻译")
			}
		} else {
			translation.SourceHash = contactNoticeSourceHash(source)
		}
		if strings.TrimSpace(translation.Text) != "" {
			out[lang] = translation
		}
	}
	return out, nil
}

func validateContactNoticeSettings(settings SiteSettings) error {
	text := settings.ContactNoticeText
	if !validContactNoticeText(text) {
		return errors.New("联系提示请使用不超过 4000 字的纯文本")
	}
	if settings.ContactNoticeEnabled && strings.TrimSpace(text) == "" {
		return errors.New("启用联系提示前请填写提示内容")
	}
	_, err := validatedContactNoticeTranslations(settings.ContactNoticeTranslations, text)
	return err
}

// Missing fields preserve the saved configuration; explicit false disables it.
// Validate the entire notice before applying it so failed patches are atomic.
func patchContactNoticeSettings(settings *SiteSettings, enabledJSON, textJSON, translationsJSON json.RawMessage) error {
	candidate := *settings
	if len(enabledJSON) > 0 {
		var enabled *bool
		if err := json.Unmarshal(enabledJSON, &enabled); err != nil || enabled == nil {
			return errors.New("联系提示开关不正确")
		}
		candidate.ContactNoticeEnabled = *enabled
	}
	if len(textJSON) > 0 {
		var text *string
		if err := json.Unmarshal(textJSON, &text); err != nil || text == nil {
			return errors.New("联系提示请使用不超过 4000 字的纯文本")
		}
		candidate.ContactNoticeText = *text
	}
	if len(translationsJSON) > 0 {
		var translations map[string]ContactNoticeTranslation
		if err := json.Unmarshal(translationsJSON, &translations); err != nil || translations == nil {
			return errors.New("联系提示译文格式不正确")
		}
		var err error
		candidate.ContactNoticeTranslations, err = validatedContactNoticeTranslations(translations, candidate.ContactNoticeText)
		if err != nil {
			return err
		}
	}
	if err := validateContactNoticeSettings(candidate); err != nil {
		return err
	}
	settings.ContactNoticeEnabled = candidate.ContactNoticeEnabled
	settings.ContactNoticeText = candidate.ContactNoticeText
	settings.ContactNoticeTranslations = candidate.ContactNoticeTranslations
	return nil
}

// Inspect only contact-notice fields so legacy backups retain their existing
// restore contract for unrelated settings. Restore validates before any writes.
func validateContactNoticeSettingsJSON(raw []byte) error {
	var fields struct {
		Enabled      json.RawMessage `json:"contact_notice_enabled"`
		Text         json.RawMessage `json:"contact_notice_text"`
		Translations json.RawMessage `json:"contact_notice_translations"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	return patchContactNoticeSettings(&SiteSettings{}, fields.Enabled, fields.Text, fields.Translations)
}

func (a *Admin) translateContactNotice(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Text     *string `json:"contact_notice_text"`
		Language string  `json:"language"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	if input.Text == nil || strings.TrimSpace(*input.Text) == "" || !validContactNoticeText(*input.Text) || !isTranslationLanguage(input.Language) {
		adminError(w, 400, "请填写不超过 4000 字的联系提示原文，并选择支持的翻译语言")
		return
	}
	if a.app.Translator == nil {
		adminError(w, 503, "翻译服务尚未启用")
		return
	}
	translated, err := a.app.Translator.translateContactNoticeText(r.Context(), *input.Text, input.Language)
	if errors.Is(err, errTranslationDisabled) {
		adminError(w, 400, "外部翻译已暂停，请在翻译设置中启用手动或自动模式")
		return
	}
	if err != nil {
		adminError(w, 400, "联系提示翻译失败，请检查翻译服务配置后重试；原文和已有译文未更改")
		return
	}
	adminJSON(w, http.StatusOK, translated)
}

func (t *Translator) translateContactNoticeText(ctx context.Context, text, language string) (ContactNoticeTranslation, error) {
	if !isTranslationLanguage(language) || !validContactNoticeText(text) || strings.TrimSpace(text) == "" {
		return ContactNoticeTranslation{}, errors.New("invalid contact notice source or language")
	}
	text = strings.TrimSpace(text)
	cfg := t.secretConfiguration()
	if cfg.ExecutionMode == translationModePaused {
		return ContactNoticeTranslation{}, errTranslationDisabled
	}
	if cfg.Endpoint == "" || cfg.Model == "" || cfg.APIKey == "" {
		return ContactNoticeTranslation{}, errors.New("translation service is not configured")
	}
	doc, err := newTranslationDocument(text, true, "contact-notice", "")
	if err != nil {
		return ContactNoticeTranslation{}, err
	}
	values := map[string]string{}
	if len(doc.slots) > 0 {
		values, err = t.translateBatch(ctx, doc.slots, language, cfg)
		if err != nil {
			return ContactNoticeTranslation{}, err
		}
	}
	if t.secretConfiguration().ExecutionMode == translationModePaused {
		return ContactNoticeTranslation{}, errTranslationDisabled
	}
	translated, err := doc.render(values)
	if err != nil || !validContactNoticeText(translated) || strings.TrimSpace(translated) == "" {
		return ContactNoticeTranslation{}, errors.New("invalid contact notice translation")
	}
	return ContactNoticeTranslation{Text: translated, SourceHash: contactNoticeSourceHash(text)}, nil
}
