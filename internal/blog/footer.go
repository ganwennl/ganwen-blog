package blog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

const footerTextMaxRunes = 4000

type FooterTranslation struct {
	Text       string `json:"text"`
	SourceHash string `json:"source_hash"`
}

func footerSourceText(settings SiteSettings) string {
	if settings.FooterText != nil {
		return *settings.FooterText
	}
	return "© " + settings.Title
}

func footerSourceHash(text string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
}

func publicFooterText(settings SiteSettings, lang string) string {
	if settings.FooterText == nil {
		return "© " + localizedPublicSettings(settings, lang).Title
	}
	source := *settings.FooterText
	if strings.TrimSpace(source) == "" {
		return ""
	}
	if translated, ok := settings.FooterTranslations[lang]; lang != "zh" && ok && translated.SourceHash == footerSourceHash(source) && strings.TrimSpace(translated.Text) != "" && validFooterText(translated.Text) {
		return translated.Text
	}
	return source
}

func validFooterText(text string) bool {
	return utf8.ValidString(text) && utf8.RuneCountInString(text) <= footerTextMaxRunes && strings.IndexFunc(text, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
	}) < 0
}

func validatedFooterTranslations(input map[string]FooterTranslation, settings SiteSettings) (map[string]FooterTranslation, error) {
	if len(input) > len(translationLanguages()) {
		return nil, errors.New("页脚译文语言不正确")
	}
	out := make(map[string]FooterTranslation, len(input))
	for lang, translation := range input {
		if !isTranslationLanguage(lang) || !validFooterText(translation.Text) {
			return nil, errors.New("页脚译文请使用支持的语言和不超过 4000 字的纯文本")
		}
		if strings.TrimSpace(translation.Text) == "" {
			continue
		}
		if translation.SourceHash == "" {
			// An explicit manual edit binds to the source submitted in this save.
			translation.SourceHash = footerSourceHash(footerSourceText(settings))
		} else if hash, err := hex.DecodeString(translation.SourceHash); err != nil || len(hash) != sha256.Size {
			return nil, errors.New("页脚译文版本不正确，请重新编辑或翻译")
		}
		out[lang] = translation
	}
	return out, nil
}

func (a *Admin) translateFooter(w http.ResponseWriter, r *http.Request) {
	var input struct {
		FooterText *string `json:"footer_text"`
		Language   string  `json:"language"`
	}
	if !readAdminJSON(w, r, &input) {
		return
	}
	if input.FooterText == nil || strings.TrimSpace(*input.FooterText) == "" || !validFooterText(*input.FooterText) || !isTranslationLanguage(input.Language) {
		adminError(w, 400, "请填写不超过 4000 字的页脚原文，并选择支持的翻译语言")
		return
	}
	if a.app.Translator == nil {
		adminError(w, 503, "翻译服务尚未启用")
		return
	}
	translated, err := a.app.Translator.translateFooterText(r.Context(), *input.FooterText, input.Language)
	if errors.Is(err, errTranslationDisabled) {
		adminError(w, 400, "外部翻译已暂停，请在翻译设置中启用手动或自动模式")
		return
	}
	if err != nil {
		adminError(w, 400, "页脚翻译失败，请检查翻译服务配置后重试；原文和已有译文未更改")
		return
	}
	adminJSON(w, http.StatusOK, translated)
}

func (t *Translator) translateFooterText(ctx context.Context, text, language string) (FooterTranslation, error) {
	if !isTranslationLanguage(language) || !validFooterText(text) || strings.TrimSpace(text) == "" {
		return FooterTranslation{}, errors.New("invalid footer source or language")
	}
	cfg := t.secretConfiguration()
	if cfg.ExecutionMode == translationModePaused {
		return FooterTranslation{}, errTranslationDisabled
	}
	if cfg.Endpoint == "" || cfg.Model == "" || cfg.APIKey == "" {
		return FooterTranslation{}, errors.New("translation service is not configured")
	}
	doc, err := newTranslationDocument(text, true, "footer", "")
	if err != nil {
		return FooterTranslation{}, err
	}
	values := map[string]string{}
	if len(doc.slots) > 0 {
		values, err = t.translateBatch(ctx, doc.slots, language, cfg)
		if err != nil {
			return FooterTranslation{}, err
		}
	}
	if t.secretConfiguration().ExecutionMode == translationModePaused {
		return FooterTranslation{}, errTranslationDisabled
	}
	translated, err := doc.render(values)
	if err != nil || !validFooterText(translated) {
		return FooterTranslation{}, errors.New("invalid footer translation")
	}
	return FooterTranslation{Text: translated, SourceHash: footerSourceHash(text)}, nil
}
