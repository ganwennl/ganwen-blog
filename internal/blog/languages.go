package blog

type blogLanguage struct {
	code, nativeName, targetName string
}

var blogLanguages = []blogLanguage{
	{"zh", "中文", "Chinese"},
	{"nl", "Nederlands", "Dutch"},
	{"en", "English", "English"},
	{"hi", "हिन्दी", "Hindi"},
	{"es", "Español", "Spanish"},
	{"ar", "العربية", "Arabic"},
	{"fr", "Français", "French"},
	{"bn", "বাংলা", "Bengali"},
	{"pt", "Português", "Portuguese"},
	{"id", "Bahasa Indonesia", "Indonesian"},
	{"ur", "اردو", "Urdu"},
}

func publicLanguages() []string {
	languages := make([]string, 0, len(blogLanguages))
	for _, language := range blogLanguages {
		languages = append(languages, language.code)
	}
	return languages
}

func translationLanguages() []string { return publicLanguages()[1:] }

func isTranslationLanguage(lang string) bool {
	for _, language := range blogLanguages[1:] {
		if language.code == lang {
			return true
		}
	}
	return false
}
func nativeLanguageName(lang string) string {
	for _, language := range blogLanguages {
		if language.code == lang {
			return language.nativeName
		}
	}
	return "中文"
}
func translationTargetName(lang string) string {
	for _, language := range blogLanguages[1:] {
		if language.code == lang {
			return language.targetName
		}
	}
	return ""
}
