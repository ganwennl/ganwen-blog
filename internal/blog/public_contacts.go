package blog

import (
	"net/url"
	"strings"
)

// Detection uses only the configured address. No server-side request is made.
func detectContactPlatform(rawURL string) (platform, label string) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "website", ""
	}
	if u.Scheme == "mailto" {
		address, _ := url.PathUnescape(u.Opaque)
		_, domain, ok := strings.Cut(address, "@")
		if !ok {
			return "email", ""
		}
		switch strings.ToLower(domain) {
		case "gmail.com", "googlemail.com":
			return "gmail", "Gmail"
		case "outlook.com", "hotmail.com", "live.com", "msn.com", "outlook.nl", "hotmail.nl":
			return "outlook", "Outlook"
		case "yahoo.com", "yahoo.co.uk", "yahoo.nl", "ymail.com", "rocketmail.com":
			return "yahoo", "Yahoo Mail"
		case "icloud.com", "me.com", "mac.com":
			return "icloud", "iCloud Mail"
		case "proton.me", "protonmail.com", "protonmail.ch", "pm.me":
			return "proton", "Proton Mail"
		case "qq.com", "foxmail.com":
			return "email", "QQ Mail"
		case "163.com", "126.com", "yeah.net":
			return "email", "网易邮箱"
		}
		return "email", ""
	}
	if u.Scheme != "https" {
		return "website", ""
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	for _, site := range []struct{ domain, platform string }{
		{"x.com", "x"}, {"twitter.com", "x"}, {"t.me", "telegram"}, {"telegram.me", "telegram"},
		{"github.com", "github"}, {"instagram.com", "instagram"}, {"youtube.com", "youtube"}, {"youtu.be", "youtube"},
		{"facebook.com", "facebook"}, {"fb.com", "facebook"}, {"linkedin.com", "linkedin"},
		{"mastodon.social", "mastodon"}, {"mastodon.online", "mastodon"}, {"mstdn.social", "mastodon"}, {"fosstodon.org", "mastodon"},
	} {
		if host == site.domain || strings.HasSuffix(host, "."+site.domain) {
			return site.platform, contactPlatformLabel(site.platform)
		}
	}
	return "website", ""
}

func contactPlatformLabel(platform string) string {
	return map[string]string{
		"x": "X", "telegram": "Telegram", "github": "GitHub", "mastodon": "Mastodon", "instagram": "Instagram",
		"youtube": "YouTube", "facebook": "Facebook", "linkedin": "LinkedIn",
	}[platform]
}

func contactIdentity(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if u.Scheme == "mailto" {
		address, _ := url.PathUnescape(u.Opaque)
		return "mailto:" + strings.ToLower(address)
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	if u.Port() == "443" && u.Scheme == "https" {
		u.Host = strings.ToLower(u.Hostname())
		if strings.Contains(u.Host, ":") {
			u.Host = "[" + u.Host + "]"
		}
	}
	u.Fragment, u.RawFragment = "", ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

func publicContactAccount(u *url.URL, platform string) string {
	if u.Scheme == "mailto" {
		address, _ := url.PathUnescape(u.Opaque)
		return address
	}
	account := strings.TrimPrefix(u.Hostname(), "www.")
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if platform == "website" || len(parts) == 0 || parts[0] == "" {
		return account
	}
	account = strings.TrimPrefix(parts[0], "@")
	if (platform == "linkedin" && (parts[0] == "in" || parts[0] == "company")) || (platform == "youtube" && (parts[0] == "channel" || parts[0] == "c" || parts[0] == "user")) || (platform == "mastodon" && parts[0] == "users") {
		if len(parts) > 1 {
			account = parts[1]
		}
	}
	if platform == "x" || platform == "telegram" || platform == "mastodon" || platform == "instagram" || (platform == "youtube" && strings.HasPrefix(parts[0], "@")) {
		account = "@" + account
	}
	if platform == "mastodon" {
		account += "@" + u.Hostname()
	}
	return account
}
