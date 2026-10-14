package blog

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Reused by the public renderer so restored or imported settings receive the
// same URL checks as settings saved through the administration API.
func normalizedSidebarAds(input SidebarAdsConfig) (SidebarAdsConfig, error) {
	if input.IntervalSeconds == 0 {
		input.IntervalSeconds = 8
	}
	if input.IntervalSeconds < 3 || input.IntervalSeconds > 120 {
		return SidebarAdsConfig{}, errors.New("广告间隔须为 3 至 120 秒")
	}
	if len(input.Items) > 12 {
		return SidebarAdsConfig{}, errors.New("广告最多 12 条")
	}
	if input.Items == nil {
		input.Items = []SidebarAd{}
	}
	if input.Items != nil {
		items := make([]SidebarAd, 0, len(input.Items))
		for _, item := range input.Items {
			item.Title = strings.TrimSpace(item.Title)
			item.ImageURL = strings.TrimSpace(item.ImageURL)
			item.TargetURL = strings.TrimSpace(item.TargetURL)
			item.Description = strings.TrimSpace(item.Description)
			item.ButtonLabel = strings.TrimSpace(item.ButtonLabel)
			item.Sponsor = strings.TrimSpace(item.Sponsor)
			if item.Title == "" || utf8.RuneCountInString(item.Title) > 120 || strings.IndexFunc(item.Title, unicode.IsControl) >= 0 {
				return SidebarAdsConfig{}, errors.New("广告标题请使用 1 至 120 字的纯文本")
			}
			if !validAvatarURL(item.ImageURL) {
				return SidebarAdsConfig{}, errors.New("广告图片请使用上传目录内的图片，或完整 HTTPS 图片地址")
			}
			if !validContactLink(ContactLink{Kind: "website", URL: item.TargetURL}) {
				return SidebarAdsConfig{}, errors.New("广告目标请使用完整 HTTPS 地址")
			}
			if !sidebarAdPlainText(item.Description, 500, true) {
				return SidebarAdsConfig{}, errors.New("广告说明请使用最多 500 字的纯文本")
			}
			if !sidebarAdPlainText(item.ButtonLabel, 40, false) {
				return SidebarAdsConfig{}, errors.New("广告按钮文字请使用最多 40 字的纯文本")
			}
			if !sidebarAdPlainText(item.Sponsor, 80, false) {
				return SidebarAdsConfig{}, errors.New("广告推广来源请使用最多 80 字的纯文本")
			}
			items = append(items, item)
		}
		input.Items = items
	}
	return input, nil
}

// These fields are rendered as text. Descriptions also accept ordinary line
// breaks and tabs entered in the administration textarea.
func sidebarAdPlainText(value string, maxRunes int, multiline bool) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxRunes && strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\r' || r == '\t'))
	}) < 0
}

// Omitted fields retain saved values. A false switch and an empty items array
// remain explicit updates, allowing older administration clients to coexist.
func patchedSidebarAds(current SidebarAdsConfig, raw json.RawMessage) (SidebarAdsConfig, error) {
	var patch *struct {
		Enabled         json.RawMessage `json:"enabled"`
		IntervalSeconds json.RawMessage `json:"interval_seconds"`
		Items           json.RawMessage `json:"items"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&patch); err != nil || patch == nil {
		return SidebarAdsConfig{}, errors.New("广告设置格式不正确")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return SidebarAdsConfig{}, errors.New("广告设置格式不正确")
	}
	for _, field := range []json.RawMessage{patch.Enabled, patch.IntervalSeconds, patch.Items} {
		if bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
			return SidebarAdsConfig{}, errors.New("广告设置格式不正确")
		}
	}
	if len(patch.Enabled) > 0 {
		if err := json.Unmarshal(patch.Enabled, &current.Enabled); err != nil {
			return SidebarAdsConfig{}, errors.New("广告开关格式不正确")
		}
	}
	if len(patch.IntervalSeconds) > 0 {
		if err := json.Unmarshal(patch.IntervalSeconds, &current.IntervalSeconds); err != nil {
			return SidebarAdsConfig{}, errors.New("广告间隔须为 3 至 120 秒")
		}
		if current.IntervalSeconds == 0 {
			return SidebarAdsConfig{}, errors.New("广告间隔须为 3 至 120 秒")
		}
	}
	if len(patch.Items) > 0 {
		var items []SidebarAd
		if err := json.Unmarshal(patch.Items, &items); err != nil {
			return SidebarAdsConfig{}, errors.New("广告列表格式不正确")
		}
		current.Items = items
	}
	return normalizedSidebarAds(current)
}
