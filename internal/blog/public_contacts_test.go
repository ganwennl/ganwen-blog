package blog

import (
	"strings"
	"testing"
)

func TestContactPlatformDetectionUsesDomainBoundaries(t *testing.T) {
	for _, test := range []struct{ address, platform, label string }{
		{"mailto:ganwen94@gmail.com", "gmail", "Gmail"},
		{"mailto:name@GOOGLEMAIL.COM", "gmail", "Gmail"},
		{"mailto:hello@hotmail.com", "outlook", "Outlook"},
		{"mailto:hello@outlook.nl", "outlook", "Outlook"},
		{"mailto:hello@proton.me", "proton", "Proton Mail"},
		{"mailto:hello@icloud.com", "icloud", "iCloud Mail"},
		{"mailto:hello@yahoo.com", "yahoo", "Yahoo Mail"},
		{"mailto:hello@gmail.com.evil.example", "email", ""},
		{"https://www.twitter.com/ganwen_nl", "x", "X"},
		{"https://t.me/contact", "telegram", "Telegram"},
		{"https://github.com/ganwen", "github", "GitHub"},
		{"https://www.instagram.com/ganwen", "instagram", "Instagram"},
		{"https://m.youtube.com/@ganwen", "youtube", "YouTube"},
		{"https://youtu.be/video", "youtube", "YouTube"},
		{"https://www.facebook.com/ganwen", "facebook", "Facebook"},
		{"https://nl.linkedin.com/in/ganwen", "linkedin", "LinkedIn"},
		{"https://fosstodon.org/@ganwen", "mastodon", "Mastodon"},
		{"https://github.com.evil.example/ganwen", "website", ""},
		{"https://notgithub.com/ganwen", "website", ""},
		{"http://github.com/ganwen", "website", ""},
	} {
		t.Run(test.address, func(t *testing.T) {
			platform, label := detectContactPlatform(test.address)
			if platform != test.platform || label != test.label {
				t.Fatalf("got %q / %q, want %q / %q", platform, label, test.platform, test.label)
			}
		})
	}
}

func TestContactRenderingPreservesAddressesAndMultipleAccounts(t *testing.T) {
	links := []ContactLink{
		{Kind: "email", URL: "mailto:ganwen94@gmail.com"},
		{Kind: "email", Label: "工作邮箱", URL: "mailto:work@outlook.com"},
		{Kind: "website", URL: "https://youtube.com/@ganwen"},
		{Kind: "website", Label: "我的网站", URL: "https://example.com:8443/about?source=contact#hello"},
		{Kind: "website", URL: "https://another.example/contact"},
		{Kind: "email", URL: "mailto:GANWEN94@GMAIL.COM"},
	}
	contacts := publicContactLinks(links, "zh")
	if len(contacts) != 5 {
		t.Fatalf("distinct email/website accounts disappeared or duplicate URL rendered: %+v", contacts)
	}
	for i, contact := range contacts {
		if contact.URL != links[i].URL {
			t.Errorf("configured destination changed: %q", contact.URL)
		}
	}
	if contacts[0].Icon != "gmail" || contacts[0].Label != "Gmail" || contacts[0].Account != "ganwen94@gmail.com" || contacts[0].NewTab {
		t.Fatalf("Gmail detection incorrect: %+v", contacts[0])
	}
	if contacts[1].Icon != "outlook" || contacts[1].Label != "工作邮箱" {
		t.Fatalf("custom label overwritten: %+v", contacts[1])
	}
	if contacts[2].Icon != "youtube" || contacts[2].Account != "@ganwen" || contacts[2].FaviconURL != "" {
		t.Fatalf("social link did not use local brand icon: %+v", contacts[2])
	}
	if contacts[3].FaviconURL != "https://example.com:8443/favicon.ico" || contacts[3].Account != "example.com" {
		t.Fatalf("website favicon did not use configured origin: %+v", contacts[3])
	}
	if contactIdentity("https://EXAMPLE.com:443") != contactIdentity("https://example.com/#anchor") {
		t.Fatal("equivalent website addresses do not deduplicate")
	}
}

func TestContactBrandIconsAreLocalAndFaviconHasFallback(t *testing.T) {
	for _, platform := range []string{"gmail", "outlook", "yahoo", "icloud", "proton", "x", "telegram", "github", "mastodon", "instagram", "youtube", "facebook", "linkedin", "email", "website"} {
		icon := string(publicContactIcon(platform))
		if !strings.Contains(icon, `<svg class="contact-icon"`) || strings.Contains(icon, "http") || strings.Contains(icon, "<script") || strings.Contains(icon, "<image") {
			t.Errorf("missing or nonlocal platform icon for %s: %s", platform, icon)
		}
	}
	a := publicTestApp(t)
	saveWidgetContacts(t, a, []ContactLink{{Kind: "website", URL: "https://example.com/about"}, {Kind: "email", URL: "mailto:ganwen94@gmail.com"}})
	body := publicTestRequest(a, "/").Body.String()
	for _, expected := range []string{`data-contact-platform="gmail"`, `data-contact-platform="website"`, `data-contact-fallback`, `data-contact-image src="https://example.com/favicon.ico"`, `referrerpolicy="no-referrer"`, `href="mailto:ganwen94@gmail.com"`} {
		if !strings.Contains(body, expected) {
			t.Errorf("missing detected icon/fallback: %s", expected)
		}
	}
}

func TestManualContactTypeOverridesSocialDetection(t *testing.T) {
	contacts := publicContactLinks([]ContactLink{
		{Kind: "website", KindManual: true, URL: "https://github.com/ganwen"},
		{Kind: "mastodon", KindManual: true, URL: "https://custom.social/@ganwen"},
		{Kind: "email", KindManual: true, URL: "mailto:ganwen94@gmail.com"},
	}, "zh")
	if len(contacts) != 3 {
		t.Fatal(contacts)
	}
	if contacts[0].Icon != "website" || contacts[0].FaviconURL != "https://github.com/favicon.ico" || contacts[0].Label != "网站" {
		t.Fatalf("manual website type ignored: %+v", contacts[0])
	}
	if contacts[1].Icon != "mastodon" || contacts[1].Account != "@ganwen@custom.social" {
		t.Fatalf("custom Mastodon instance ignored: %+v", contacts[1])
	}
	if contacts[2].Icon != "gmail" || contacts[2].Label != "Gmail" {
		t.Fatalf("email provider branding lost: %+v", contacts[2])
	}
}
