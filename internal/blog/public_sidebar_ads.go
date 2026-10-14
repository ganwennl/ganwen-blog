package blog

import (
	"net/http"
	"strconv"
)

const publicSidebarAdCookieName = "ganwen.sidebar-ad.v1"

func nextPublicSidebarAdIndex(r *http.Request, count int) int {
	cookie, err := r.Cookie(publicSidebarAdCookieName)
	if err != nil || cookie.Value == "" {
		return 0
	}
	for _, char := range cookie.Value {
		if char < '0' || char > '9' {
			return 0
		}
	}
	index, err := strconv.ParseUint(cookie.Value, 10, 64)
	if err != nil {
		return 0
	}
	// Reduce before adding to remain safe after items are removed or reordered.
	return int((index%uint64(count) + 1) % uint64(count))
}

func (a *App) populatePublicSidebarAd(w http.ResponseWriter, r *http.Request, view *publicView, settings SiteSettings) {
	view.SidebarAd = nil
	ads, err := normalizedSidebarAds(settings.SidebarAds)
	if err != nil || !ads.Enabled || len(ads.Items) == 0 {
		return
	}
	index := nextPublicSidebarAdIndex(r, len(ads.Items))
	ad := ads.Items[index]
	view.SidebarAd = &ad
	if r.Method == http.MethodGet {
		http.SetCookie(w, &http.Cookie{
			Name: publicSidebarAdCookieName, Value: strconv.Itoa(index), Path: "/",
			MaxAge: 30 * 24 * 60 * 60, HttpOnly: true, Secure: !a.Config.Preview, SameSite: http.SameSiteLaxMode,
		})
	}
}
