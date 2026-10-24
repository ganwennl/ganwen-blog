package blog

import (
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"

	xhtml "golang.org/x/net/html"
)

func TestTranslationQuantitiesExpandExactlyAndPreserveApproximation(t *testing.T) {
	for _, test := range []struct {
		name, source, want string
	}{
		{"population", "50余万人", "500000余人"},
		{"large-payment", "4000余万元", "40000000余元"},
		{"approximate-spaces", "约 105 万元", "约 1050000元"},
		{"hundred-million", "1亿元", "100000000元"},
		{"decimal", "1.05亿", "105000000"},
		{"sub-unit", "0.00001万", "0.1"},
		{"compound-scale", "1万亿", "1000000000000"},
		{"above-float-integer-precision", "9007199254740993万", "90071992547409930000"},
		{"above-float-fraction-precision", "0.10000000000000001万", "1000.0000000000001"},
		{"trailing-fraction-zeroes", "1.0500亿", "105000000"},
		{"zero", "0万元", "0元"},
		{"prose", "人数50余万人，金额4000余万元，约 105 万元。", "人数500000余人，金额40000000余元，约 1050000元。"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeTranslationQuantities(test.source); got != test.want {
				t.Fatalf("quantity normalization: %q -> %q, want %q", test.source, got, test.want)
			}
		})
	}
}

func TestTranslationQuantitiesDoNotRewriteURLsMailIdentifiersOrKeepTokens(t *testing.T) {
	for _, source := range []string{
		"https://example.test/50万?q=105万元",
		"查看 https://example.test/50万?q=105万元 获取文件",
		"联系 billing105@example.test 获取记录",
		"版本 v1.05亿、Model105万 和 API4000万元 保持不变",
		"已有 ⟪GW_KEEP_10000⟫ 和 ⟪GW_KEEP_1⟫，保留标记",
	} {
		if got := normalizeTranslationQuantities(source); got != source {
			t.Fatalf("protected text changed: %q -> %q", source, got)
		}
	}
}

func TestTranslationQuantityNormalizationPrecedesNumberProtection(t *testing.T) {
	source := "人数50余万人，金额4000余万元，约 105 万元，累计1.05亿元。"
	doc, err := newTranslationDocument(source, true, "quantity", "")
	if err != nil || len(doc.slots) == 0 {
		t.Fatalf("quantity document: %v", err)
	}
	values := make(map[string]string)
	var outgoing strings.Builder
	for _, slot := range doc.slots {
		values[slot.ID] = slot.Text
		outgoing.WriteString(slot.Text)
	}
	if strings.ContainsAny(outgoing.String(), "万亿") {
		t.Fatalf("provider still sees a hidden number followed by a Chinese scale: %s", outgoing.String())
	}
	if !strings.Contains(outgoing.String(), "余") || !strings.Contains(outgoing.String(), "约") {
		t.Fatal("quantity approximation was removed before translation")
	}
	got, err := doc.render(values)
	want := "人数500000余人，金额40000000余元，约 1050000元，累计105000000元。"
	if err != nil || got != want {
		t.Fatalf("canonical protected quantities: %q %v; want %q", got, err, want)
	}
}

// Compare element order and every non-text attribute independently of quoting.
// A translated alt/title may change; URL, event, data and media attributes may not.
func translationAttributeShape(t *testing.T, body string) ([]string, []map[string]string) {
	t.Helper()
	z := xhtml.NewTokenizer(strings.NewReader(body))
	var shape []string
	var attributes []map[string]string
	for {
		kind := z.Next()
		if kind == xhtml.ErrorToken {
			if z.Err() != io.EOF {
				t.Fatal(z.Err())
			}
			return shape, attributes
		}
		if kind != xhtml.StartTagToken && kind != xhtml.SelfClosingTagToken && kind != xhtml.EndTagToken {
			continue
		}
		token := z.Token()
		all := make(map[string]string)
		var fixed []string
		for _, attr := range token.Attr {
			all[attr.Key] = attr.Val
			if attr.Key != "alt" && attr.Key != "title" {
				fixed = append(fixed, attr.Key+"="+attr.Val)
			}
		}
		sort.Strings(fixed)
		shape = append(shape, kind.String()+":"+token.Data+":"+strings.Join(fixed, "|"))
		if kind != xhtml.EndTagToken {
			attributes = append(attributes, all)
		}
	}
}

func TestTranslationHumanReadableAttributesTranslateWithoutChangingLinksOrOtherAttributes(t *testing.T) {
	source := `<article data-title="中文配置" title='文章说明'><a href="/50万?q=105万元&amp;b=1亿元" title="查看中文" onclick="window.fake='title=中文';">中文正文</a><img alt=中文图片 src="/50万.jpg" width="300" data-note="中文属性"></article>`
	doc, err := newTranslationDocument(source, false, "body", "")
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string)
	replacer := strings.NewReplacer("文章说明", "Article description", "查看中文", "Read translation", "中文正文", "Translated body", "中文图片", "Example image")
	for _, slot := range doc.slots {
		if strings.Contains(slot.Text, "中文配置") || strings.Contains(slot.Text, "中文属性") || strings.Contains(slot.Text, "title=中文") {
			t.Fatalf("a non-text attribute was sent for translation: %s", slot.Text)
		}
		values[slot.ID] = replacer.Replace(slot.Text)
	}
	got, err := doc.render(values)
	if err != nil {
		t.Fatal(err)
	}
	wantShape, _ := translationAttributeShape(t, source)
	gotShape, attrs := translationAttributeShape(t, got)
	if !reflect.DeepEqual(wantShape, gotShape) {
		t.Fatalf("HTML or non-text attributes changed:\n%s", got)
	}
	if len(attrs) != 3 || attrs[0]["title"] != "Article description" || attrs[1]["title"] != "Read translation" || attrs[2]["alt"] != "Example image" {
		t.Fatalf("human-readable attributes were not translated: %s", got)
	}
	for _, raw := range []string{`href="/50万?q=105万元&amp;b=1亿元"`, `onclick="window.fake='title=中文';"`, `src="/50万.jpg"`, `data-title="中文配置"`, `data-note="中文属性"`, `width="300"`} {
		if !strings.Contains(got, raw) {
			t.Fatalf("non-text attribute lost its original bytes %q: %s", raw, got)
		}
	}
	if !strings.Contains(got, "Translated body") {
		t.Fatal("attribute handling omitted the regular article text")
	}
}

func TestTranslationAttributeValuesCannotInjectMarkupOrEventAttributes(t *testing.T) {
	source := `<img alt='中文描述' src='/safe.png'><a title=中文提示 href='/safe'>正文</a>`
	doc, err := newTranslationDocument(source, false, "body", "")
	if err != nil {
		t.Fatal(err)
	}
	attack := `"quoted" onerror="evil()" 'single' <script>alert(1)</script> & text`
	values := make(map[string]string)
	for _, slot := range doc.slots {
		values[slot.ID] = attack
	}
	got, err := doc.render(values)
	if err != nil {
		t.Fatal(err)
	}
	wantShape, _ := translationAttributeShape(t, source)
	gotShape, attrs := translationAttributeShape(t, got)
	if !reflect.DeepEqual(wantShape, gotShape) || len(attrs) != 2 {
		t.Fatalf("translated attribute injected HTML or an event attribute: %s", got)
	}
	if attrs[0]["alt"] != attack || attrs[1]["title"] != attack {
		t.Fatalf("translated attribute was truncated or escaped incorrectly: %s", got)
	}
	if strings.Contains(got, "<script>") {
		t.Fatal("translated text created an executable element")
	}
}

func TestTranslationProtectedElementsKeepTextAttributesAndQuantitiesVerbatim(t *testing.T) {
	protected := []string{
		`<pre title='中文段'><img alt='中文图' src='/50万.jpg'>105万元</pre>`,
		`<code title='中文代码'>105万元 &lt;tag&gt;</code>`,
		`<script title='中文脚本'>const name = "105万元";</script>`,
		`<style title='中文样式'>.中文 { --money: "50万"; }</style>`,
		`<textarea title='中文输入'>中文50万元</textarea>`,
		`<kbd title='中文键'>105万元</kbd>`,
		`<samp title='中文例'>105万元</samp>`,
		`<svg title='中文图'><title>50万元</title><text>105万元</text></svg>`,
		`<math title='中文公式'><mi>105万元</mi></math>`,
	}
	source := strings.Join(protected, "") + `<p>中文正文</p>`
	doc, err := newTranslationDocument(source, false, "body", "")
	if err != nil || len(doc.slots) != 1 {
		t.Fatalf("protected elements generated translation requests: slots=%d err=%v", len(doc.slots), err)
	}
	got, err := doc.render(map[string]string{doc.slots[0].ID: "Translated article"})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range protected {
		if !strings.Contains(got, raw) {
			t.Fatalf("protected text or attributes changed: %s", raw)
		}
	}
	if !strings.Contains(got, `<p>Translated article</p>`) {
		t.Fatal("protected elements swallowed the later article text")
	}
}
