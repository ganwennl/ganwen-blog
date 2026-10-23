package blog

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const contactNoticeMaxRunes = 4000

func validateContactNoticeSettings(settings SiteSettings) error {
	text := settings.ContactNoticeText
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > contactNoticeMaxRunes || strings.IndexFunc(text, func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
	}) >= 0 {
		return errors.New("联系提示请使用不超过 4000 字的纯文本")
	}
	if settings.ContactNoticeEnabled && strings.TrimSpace(text) == "" {
		return errors.New("启用联系提示前请填写提示内容")
	}
	return nil
}

// Missing fields preserve the saved configuration; explicit false disables it.
// Validate the pair before applying either value so failed patches are atomic.
func patchContactNoticeSettings(settings *SiteSettings, enabledJSON, textJSON json.RawMessage) error {
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
	if err := validateContactNoticeSettings(candidate); err != nil {
		return err
	}
	settings.ContactNoticeEnabled = candidate.ContactNoticeEnabled
	settings.ContactNoticeText = candidate.ContactNoticeText
	return nil
}

// Inspect only contact-notice fields so legacy backups retain their existing
// restore contract for unrelated settings. Restore validates before any writes.
func validateContactNoticeSettingsJSON(raw []byte) error {
	var fields struct {
		Enabled json.RawMessage `json:"contact_notice_enabled"`
		Text    json.RawMessage `json:"contact_notice_text"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	return patchContactNoticeSettings(&SiteSettings{}, fields.Enabled, fields.Text)
}
