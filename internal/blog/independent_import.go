package blog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"

	"golang.org/x/net/html"
)

type independentPageFile struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type independentPagePackage struct {
	Schema    int                   `json:"schema"`
	SourceURL string                `json:"source_url"`
	Post      Post                  `json:"post"`
	Source    independentPageFile   `json:"source"`
	Media     []independentPageFile `json:"media"`
}

// ImportIndependentPageFile adds one prepared page to an existing installation.
// It verifies every original before publishing, preserves original HTML, and
// never replaces a managed page or an existing media file with different bytes.
func (s *Store) ImportIndependentPageFile(ctx context.Context, file, mediaRoot string) (int64, bool, error) {
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return 0, false, errors.New("independent page package is missing or too large")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return 0, false, errors.New("cannot read independent page package")
	}
	var prepared independentPagePackage
	if err = json.Unmarshal(raw, &prepared); err != nil {
		return 0, false, errors.New("invalid independent page package")
	}
	p := prepared.Post
	if prepared.Schema != 1 || p.Type != "page" || p.Status != "publish" || !p.Unlisted || p.PublicPath == "" || p.ID != 0 || len(prepared.Media) == 0 || len(prepared.Media) > 1024 {
		return 0, false, errors.New("package must contain one published unlisted page and its evidence")
	}
	p.ID = 1 // Validate a new document before assigning its managed ID.
	if err = validPost(p); err != nil {
		return 0, false, err
	}
	p.ID = 0
	if strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.ContentHTML) == "" {
		return 0, false, errors.New("original page content is empty")
	}
	sourceURL, err := url.Parse(prepared.SourceURL)
	if err != nil || sourceURL.Scheme != "https" || sourceURL.Hostname() == "" || sourceURL.User != nil || sourceURL.Path != p.PublicPath {
		return 0, false, errors.New("original page URL does not match its preserved public path")
	}
	if prepared.Source.Path != "source.html" || prepared.Source.Bytes != int64(len(p.SourceHTML)) || prepared.Source.SHA256 != independentBytesHash([]byte(p.SourceHTML)) {
		return 0, false, errors.New("original HTML does not match the source manifest")
	}
	packageRoot := filepath.Dir(file)
	sourcePath, err := publicMediaPath(packageRoot, "/source.html")
	if err != nil || independentVerifyFile(sourcePath, prepared.Source) != nil {
		return 0, false, errors.New("retained original HTML checksum mismatch")
	}
	manifest := make(map[string]independentPageFile, len(prepared.Media))
	casePaths := make(map[string]bool, len(prepared.Media))
	for _, entry := range prepared.Media {
		if !independentSafeMediaName(entry.Path) || entry.Bytes <= 0 || !independentValidSHA(entry.SHA256) || casePaths[strings.ToLower(entry.Path)] {
			return 0, false, errors.New("invalid or duplicate evidence manifest entry")
		}
		casePaths[strings.ToLower(entry.Path)] = true
		manifest[entry.Path] = entry
		source, e := publicMediaPath(filepath.Join(packageRoot, "media"), "/"+entry.Path)
		if e != nil || independentVerifyFile(source, entry) != nil {
			return 0, false, errors.New("evidence is missing, unsafe or has a checksum mismatch")
		}
	}
	if err = independentVerifyReferences(p, sourceURL, manifest); err != nil {
		return 0, false, err
	}
	posts, err := s.ListPosts(ctx, true)
	if err != nil {
		return 0, false, err
	}
	var existingID int64
	for _, current := range posts {
		// SourceHTML is immutable after import. A renamed or edited managed page
		// still owns this original, even when its current public path has changed.
		if current.SourceHTML == p.SourceHTML && current.PublicPath != p.PublicPath {
			return 0, false, errors.New("original evidence is already managed at a changed address; refusing to recreate its previous page")
		}
		if current.PublicPath != p.PublicPath {
			continue
		}
		if existingID != 0 || !independentSamePage(current, p) {
			return 0, false, errors.New("existing page at this address differs; refusing to replace managed content")
		}
		existingID = current.ID
	}
	mediaRoot, err = filepath.Abs(mediaRoot)
	if err != nil || os.MkdirAll(mediaRoot, 0700) != nil {
		return 0, false, errors.New("cannot prepare destination media directory")
	}
	// Check every destination before creating anything. A conflicting original
	// must not result in a partially copied package or a published page.
	for _, entry := range prepared.Media {
		target, e := independentDestination(mediaRoot, entry.Path)
		if e != nil {
			return 0, false, e
		}
		if _, e = os.Stat(target); e == nil {
			if independentVerifyFile(target, entry) != nil {
				return 0, false, errors.New("existing destination evidence differs; refusing to overwrite")
			}
		} else if !os.IsNotExist(e) {
			return 0, false, errors.New("cannot inspect destination evidence")
		}
	}
	for _, entry := range prepared.Media {
		source, e := publicMediaPath(filepath.Join(packageRoot, "media"), "/"+entry.Path)
		if e != nil || independentCopyFile(source, mediaRoot, entry) != nil {
			return 0, false, errors.New("could not safely install verified evidence; page was not imported")
		}
	}
	if existingID != 0 {
		return existingID, false, nil
	}
	if err = s.NewPost(ctx, &p); err != nil {
		return 0, false, err
	}
	return p.ID, true, nil
}

func independentBytesHash(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func independentValidSHA(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func independentSafeMediaName(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\\:\x00") || path.Clean(value) != value || value == ".." || strings.HasPrefix(value, "../") {
		return false
	}
	_, ok := publicMediaTypes[strings.ToLower(path.Ext(value))]
	return ok
}

func independentVerifyFile(file string, entry independentPageFile) error {
	f, err := os.Open(file)
	if err != nil {
		return errors.New("cannot read verified file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != entry.Bytes {
		return errors.New("verified file size differs")
	}
	digest := sha256.New()
	if _, err = io.Copy(digest, f); err != nil || hex.EncodeToString(digest.Sum(nil)) != entry.SHA256 {
		return errors.New("verified file checksum differs")
	}
	return nil
}

func independentVerifyReferences(p Post, sourceURL *url.URL, manifest map[string]independentPageFile) error {
	host := strings.TrimPrefix(strings.ToLower(sourceURL.Hostname()), "www.")
	var bodies []string
	bodies = append(bodies, p.ContentHTML)
	for _, translation := range p.Translations {
		bodies = append(bodies, translation.ContentHTML)
	}
	for _, body := range bodies {
		document, err := html.Parse(strings.NewReader(body))
		if err != nil {
			return errors.New("cannot parse migrated page evidence")
		}
		var walk func(*html.Node) error
		walk = func(node *html.Node) error {
			for _, attr := range node.Attr {
				if attr.Key != "src" && attr.Key != "href" && attr.Key != "poster" {
					continue
				}
				u, err := url.Parse(attr.Val)
				if err != nil {
					return errors.New("invalid evidence reference")
				}
				resolved := sourceURL.ResolveReference(u)
				if strings.TrimPrefix(strings.ToLower(resolved.Hostname()), "www.") != host {
					continue // External citations remain links rather than copied media.
				}
				if _, media := publicMediaTypes[strings.ToLower(path.Ext(resolved.Path))]; !media {
					continue
				}
				if _, ok := manifest[strings.TrimPrefix(resolved.Path, "/")]; !ok {
					return errors.New("page references evidence absent from its complete manifest")
				}
			}
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				if err := walk(child); err != nil {
					return err
				}
			}
			return nil
		}
		if err := walk(document); err != nil {
			return err
		}
	}
	return nil
}

func independentSamePage(a, b Post) bool {
	// Managed IDs and edit timestamps are assigned by the store. Human-provided
	// translated text and its source hash remain part of the equality contract.
	normalize := func(p Post) Post {
		p.ID, p.Version, p.AuthorID = 0, 0, 0
		p.PublishedAt, p.ModifiedAt, p.Author, p.CanonicalURL = "", "", "", ""
		p.Translations = clonePost(p, false).Translations
		if len(p.Translations) == 0 {
			p.Translations = nil
		}
		for language, translation := range p.Translations {
			translation.UpdatedAt = ""
			p.Translations[language] = translation
		}
		return p
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}

func independentDestination(root, relative string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.New("invalid destination media root")
	}
	target := filepath.Join(realRoot, filepath.FromSlash(relative))
	current := realRoot
	for _, segment := range strings.Split(relative, "/") {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("unsafe destination media path")
		}
	}
	return target, nil
}

func independentCopyFile(source, root string, entry independentPageFile) error {
	target, err := independentDestination(root, entry.Path)
	if err != nil {
		return err
	}
	if _, err = os.Stat(target); err == nil {
		return independentVerifyFile(target, entry)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return independentVerifyFile(target, entry)
	}
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil || independentVerifyFile(target, entry) != nil {
		_ = os.Remove(target) // Only the file created by this exclusive open.
		return errors.New("evidence copy checksum mismatch")
	}
	return nil
}
