package blog

import (
	"encoding/json"
	"errors"
	"net/http"
)

var sidebarModuleOrder = [...]string{"search", "popular", "stats", "archives", "categories", "tags"}
var defaultSidebarOrder = [...]string{"search", "popular", "stats", "archives", "ads", "categories", "tags"}

func validSidebarOrder(input []string) bool {
	if len(input) != len(defaultSidebarOrder) {
		return false
	}
	seen := make(map[string]bool, len(input))
	for _, item := range input {
		if !sidebarModuleSelected(defaultSidebarOrder[:], item) || seen[item] {
			return false
		}
		seen[item] = true
	}
	return true
}

func savedSidebarOrder(settings SiteSettings) []string {
	if validSidebarOrder(settings.SidebarOrder) {
		return append([]string{}, settings.SidebarOrder...)
	}
	return append([]string{}, defaultSidebarOrder[:]...)
}

// Switching back to defaults preserves the saved custom order for later use.
func effectiveSidebarOrder(settings SiteSettings) []string {
	if settings.SidebarCustomOrderEnabled {
		return savedSidebarOrder(settings)
	}
	return append([]string{}, defaultSidebarOrder[:]...)
}

func parsedSidebarOrder(raw json.RawMessage) ([]string, error) {
	var order []string
	if err := json.Unmarshal(raw, &order); err != nil || !validSidebarOrder(order) {
		return nil, errors.New("侧栏排序必须包含全部 7 个组件，且不能重复")
	}
	return order, nil
}

func parsedSidebarCustomOrderEnabled(raw json.RawMessage) (bool, error) {
	var enabled *bool
	if err := json.Unmarshal(raw, &enabled); err != nil || enabled == nil {
		return false, errors.New("侧栏排序开关不正确")
	}
	return *enabled, nil
}

func patchSidebarOrdering(settings *SiteSettings, enabled, order json.RawMessage) error {
	if len(enabled) > 0 {
		value, err := parsedSidebarCustomOrderEnabled(enabled)
		if err != nil {
			return err
		}
		settings.SidebarCustomOrderEnabled = value
	}
	if len(order) > 0 {
		value, err := parsedSidebarOrder(order)
		if err != nil {
			return err
		}
		settings.SidebarOrder = value
	}
	return nil
}

func sidebarModuleSelected(modules []string, wanted string) bool {
	for _, module := range modules {
		if module == wanted {
			return true
		}
	}
	return false
}

// Legacy settings show every module. Imported unknown IDs are never rendered,
// and the existing public statistics switch continues to hide statistics.
func publicSidebarModules(settings SiteSettings) []string {
	modules := make([]string, 0, len(sidebarModuleOrder))
	for _, module := range sidebarModuleOrder {
		if module == "stats" && !publicStatsEnabled(settings) {
			continue
		}
		if settings.SidebarModules == nil || sidebarModuleSelected(*settings.SidebarModules, module) {
			modules = append(modules, module)
		}
	}
	return modules
}

func sidebarModulesWithStats(input []string, enabled bool) []string {
	modules := make([]string, 0, len(sidebarModuleOrder))
	for _, module := range sidebarModuleOrder {
		if (module == "stats" && enabled) || (module != "stats" && sidebarModuleSelected(input, module)) {
			modules = append(modules, module)
		}
	}
	return modules
}

func parsedSidebarModules(raw json.RawMessage) ([]string, error) {
	var input []string
	if err := json.Unmarshal(raw, &input); err != nil || input == nil {
		return nil, errors.New("侧栏模块请使用列表，可留空但不能为 null")
	}
	seen := make(map[string]bool, len(input))
	for _, module := range input {
		valid := false
		for _, allowed := range sidebarModuleOrder {
			if allowed == module {
				valid = true
				break
			}
		}
		if !valid || seen[module] {
			return nil, errors.New("侧栏模块包含未知或重复选项")
		}
		seen[module] = true
	}
	modules := make([]string, 0, len(input))
	for _, module := range sidebarModuleOrder {
		if seen[module] {
			modules = append(modules, module)
		}
	}
	return modules, nil
}

// Validate sidebar fields before restoring any rows, while preserving legacy
// backups that omitted sidebar settings or use unrelated old site fields.
func validateSidebarModulesJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	if modules, supplied := fields["sidebar_modules"]; supplied {
		if _, err := parsedSidebarModules(modules); err != nil {
			return err
		}
	}
	return patchSidebarOrdering(&SiteSettings{}, fields["sidebar_custom_order_enabled"], fields["sidebar_order"])
}

type sidebarSettingsResponse struct {
	Modules               []string         `json:"modules"`
	ArticleSidebarEnabled bool             `json:"article_sidebar_enabled"`
	SidebarAds            SidebarAdsConfig `json:"sidebar_ads"`
	CustomOrderEnabled    bool             `json:"custom_order_enabled"`
	Order                 []string         `json:"order"`
}

func (a *Admin) sidebar(w http.ResponseWriter, r *http.Request) {
	current, err := a.app.Store.Settings(r.Context())
	if err != nil {
		storeError(w, err)
		return
	}
	if r.Method == http.MethodPut {
		var input *struct {
			Modules               json.RawMessage `json:"modules"`
			ArticleSidebarEnabled *bool           `json:"article_sidebar_enabled"`
			SidebarAds            json.RawMessage `json:"sidebar_ads"`
			CustomOrderEnabled    json.RawMessage `json:"custom_order_enabled"`
			Order                 json.RawMessage `json:"order"`
		}
		if !readAdminJSON(w, r, &input) {
			return
		}
		if input == nil || input.ArticleSidebarEnabled == nil || len(input.SidebarAds) == 0 {
			adminError(w, 400, "请提供完整侧栏设置")
			return
		}
		modules, err := parsedSidebarModules(input.Modules)
		if err != nil {
			adminError(w, 400, err.Error())
			return
		}
		ads, err := patchedSidebarAds(current.SidebarAds, input.SidebarAds)
		if err != nil {
			adminError(w, 400, err.Error())
			return
		}
		current.SidebarModules = &modules
		if err := patchSidebarOrdering(&current, input.CustomOrderEnabled, input.Order); err != nil {
			adminError(w, 400, err.Error())
			return
		}
		current.ArticleSidebarEnabled = *input.ArticleSidebarEnabled
		current.SidebarAds = ads
		showStats := sidebarModuleSelected(modules, "stats")
		current.PublicStatsEnabled = &showStats
		if err := a.app.Store.SaveSettings(r.Context(), current); err != nil {
			storeError(w, err)
			return
		}
	}
	ads, err := normalizedSidebarAds(current.SidebarAds)
	if err != nil {
		storeError(w, err)
		return
	}
	adminJSON(w, http.StatusOK, sidebarSettingsResponse{
		Modules:               publicSidebarModules(current),
		ArticleSidebarEnabled: current.ArticleSidebarEnabled,
		SidebarAds:            ads,
		CustomOrderEnabled:    current.SidebarCustomOrderEnabled,
		Order:                 savedSidebarOrder(current),
	})
}
