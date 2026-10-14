package blog

import (
	"encoding/json"
	"errors"
)

const publicDailyViewsDefault int64 = 1000
const publicDailyViewsMax int64 = 1000000

// Existing sites retain their presentation until the owner changes this flag.
func publicVirtualViewsEnabled(settings SiteSettings) bool {
	return settings.PublicVirtualViewsEnabled == nil || *settings.PublicVirtualViewsEnabled
}

func publicStatsEnabled(settings SiteSettings) bool {
	return settings.PublicStatsEnabled == nil || *settings.PublicStatsEnabled
}

func parsedPublicVirtualViewsEnabled(raw json.RawMessage) (*bool, error) {
	var enabled *bool
	if err := json.Unmarshal(raw, &enabled); err != nil || enabled == nil {
		return nil, errors.New("虚拟访问量开关不正确")
	}
	return enabled, nil
}

func validatePublicDailyViews(value *int64) error {
	if value != nil && (*value < 0 || *value > publicDailyViewsMax) {
		return errors.New("每日展示浏览量请填写 0 至 1000000 的整数")
	}
	return nil
}

// Missing legacy settings and invalid imported values use the display default.
// An explicit zero is preserved. This never changes real analytics or settings.
func publicDailyDisplayTarget(settings SiteSettings) int64 {
	if settings.PublicDailyViews == nil || validatePublicDailyViews(settings.PublicDailyViews) != nil {
		return publicDailyViewsDefault
	}
	return *settings.PublicDailyViews
}

func parsedPublicDailyViews(raw json.RawMessage) (*int64, error) {
	var value *int64
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, errors.New("每日展示浏览量请填写 0 至 1000000 的整数")
	}
	if err := validatePublicDailyViews(value); err != nil {
		return nil, err
	}
	return value, nil
}

// Backups store site settings as a JSON string inside their key/value rows.
// Validate only the new field before restore writes any rows, without changing
// the existing backup semantics for other site fields or legacy site documents.
func validatePublicTrafficSettingsJSON(raw []byte) error {
	var settings struct {
		PublicDailyViews          json.RawMessage `json:"public_daily_views"`
		PublicVirtualViewsEnabled json.RawMessage `json:"public_virtual_views_enabled"`
		PublicStatsEnabled        json.RawMessage `json:"public_stats_enabled"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil
	}
	if len(settings.PublicVirtualViewsEnabled) > 0 {
		if _, err := parsedPublicVirtualViewsEnabled(settings.PublicVirtualViewsEnabled); err != nil {
			return err
		}
	}
	if len(settings.PublicDailyViews) > 0 {
		if _, err := parsedPublicDailyViews(settings.PublicDailyViews); err != nil {
			return err
		}
	}
	if len(settings.PublicStatsEnabled) > 0 {
		_, err := parsedPublicVirtualViewsEnabled(settings.PublicStatsEnabled)
		return err
	}
	return nil
}
