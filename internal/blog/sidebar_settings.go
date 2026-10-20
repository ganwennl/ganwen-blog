package blog

import (
	"encoding/json"
	"errors"
	"net/http"
)

var sidebarModuleOrder = [...]string{"search", "popular", "stats", "archives", "categories", "tags"}

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

// Validate this field before restoring any rows, while preserving legacy
// backups that omitted sidebar settings or use unrelated old site fields.
func validateSidebarModulesJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	if modules, supplied := fields["sidebar_modules"]; supplied {
		_, err := parsedSidebarModules(modules)
		return err
	}
	return nil
}

type sidebarSettingsResponse struct {
	Modules               []string         `json:"modules"`
	ArticleSidebarEnabled bool             `json:"article_sidebar_enabled"`
	SidebarAds            SidebarAdsConfig `json:"sidebar_ads"`
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
	})
}
