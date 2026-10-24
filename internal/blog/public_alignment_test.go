package blog

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func alignmentTestElement(t *testing.T, input, id string) *html.Node {
	t.Helper()
	nodes, err := html.ParseFragment(strings.NewReader(input), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		t.Fatal(err)
	}
	var found *html.Node
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		for _, attr := range node.Attr {
			if attr.Key == "id" && attr.Val == id {
				found = node
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	for _, node := range nodes {
		visit(node)
	}
	if found == nil {
		t.Fatalf("missing element %q: %s", id, input)
	}
	return found
}

func alignmentTestAttr(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func TestPublicAlignmentUsesBrowserPrecedence(t *testing.T) {
	for _, tt := range []struct {
		name, attributes, want string
	}{
		{"style before legacy", `style="text-align:center" align="left"`, "text-align-center"},
		{"style after legacy", `align="left" style="text-align:center"`, "text-align-center"},
		{"style overrides old class", `class="has-text-align-right text-align-justify retained" style="text-align:center"`, "retained text-align-center"},
		{"class overrides legacy", `class="has-text-align-left retained" align="right"`, "has-text-align-left retained"},
		{"important before normal", `style="text-align:center!important;text-align:left"`, "text-align-center"},
		{"important after normal", `style="text-align:left;text-align:center ! important"`, "text-align-center"},
		{"last important wins", `style="text-align:left!important;text-align:right!important"`, "text-align-right"},
		{"last valid normal wins", `style="text-align:center;text-align:right;text-align:expression(alert(1))"`, "text-align-right"},
		{"invalid style keeps class", `class="has-text-align-center" style="text-align:url(https://bad.example/a)" align="right"`, "has-text-align-center"},
		{"logical start", `dir="rtl" style="text-align:start" align="left"`, "text-align-start"},
		{"logical end", `dir="ltr" style="text-align:end"`, "text-align-end"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := string(safePublicHTML(`<div id="sample" `+tt.attributes+`>内容</div>`, "https://www.ganwen.nl"))
			node := alignmentTestElement(t, out, "sample")
			if got := alignmentTestAttr(node, "class"); got != tt.want {
				t.Fatalf("alignment classes %q; want %q: %s", got, tt.want, out)
			}
			for _, bad := range []string{`style=`, `align=`, `expression`, `bad.example`} {
				if strings.Contains(out, bad) {
					t.Fatalf("unsafe or obsolete formatting retained: %s", out)
				}
			}
			if strings.Contains(tt.attributes, `dir="rtl"`) && alignmentTestAttr(node, "dir") != "rtl" {
				t.Fatalf("logical alignment lost its direction: %s", out)
			}
		})
	}
}

func TestPublicLegacyCenterAndChildAlignment(t *testing.T) {
	input := `<center id="legacy"><p>继承居中</p><div id="child" style="text-align:left">明确左对齐</div><img id="photo" src="/photo.png" width="400" height="200"></center><center id="styled" style="text-align:right"><img alt="🤣" src="https://abs.twimg.com/emoji/v2/svg/1f923.svg"></center>`
	out := string(safePublicHTML(input, "https://www.ganwen.nl"))
	for id, want := range map[string]string{"legacy": "text-align-center", "child": "text-align-left", "styled": "text-align-right"} {
		node := alignmentTestElement(t, out, id)
		if node.Data != "div" || alignmentTestAttr(node, "class") != want {
			t.Fatalf("legacy or nested alignment changed for %s: %s", id, out)
		}
	}
	photo := alignmentTestElement(t, out, "photo")
	if alignmentTestAttr(photo, "width") != "400" || alignmentTestAttr(photo, "height") != "200" || alignmentTestAttr(photo, "src") != "/photo.png" || !strings.Contains(out, "https://abs.twimg.com/emoji/v2/svg/1f923.svg") {
		t.Fatalf("media changed while normalizing alignment: %s", out)
	}
}

func TestPublicPastedNestedFontBlocksKeepAlignment(t *testing.T) {
	// Reproduces the nesting of pasted rich text without retaining article text.
	input := `<div id="first" style="text-align:center"><span style="white-space:pre-wrap;color:red">第一段</span></div><span class="external" style="display:inline"><div style="text-align:center"><font face="External"><span><br></span></font></div><font face="External"><div id="second" style="color:red;text-align:center"><span>第二段</span></div><div id="third" style="text-align:center"><span class="external"><font face="External"><font face="system-ui"><span style="white-space-collapse:preserve">       第三段</span></font><span>文字</span><font face="system-ui"><span>结束</span></font></font></span><span><img alt="🤣" src="https://abs.twimg.com/emoji/v2/svg/1f923.svg"></span></div></font></span><span><font face="External"><div id="last" style="text-align:center"><span>最后一段</span></div></font></span>`
	out := string(safePublicHTML(input, "https://www.ganwen.nl"))
	for _, id := range []string{"first", "second", "third", "last"} {
		node := alignmentTestElement(t, out, id)
		if alignmentTestAttr(node, "class") != "text-align-center" {
			t.Fatalf("nested block lost alignment for %s: %s", id, out)
		}
	}
	for _, text := range []string{"第一段", "第二段", "第三段", "文字", "结束", "最后一段", "https://abs.twimg.com/emoji/v2/svg/1f923.svg"} {
		if !strings.Contains(out, text) {
			t.Fatalf("pasted content lost %q: %s", text, out)
		}
	}
	if strings.Contains(out, "<font") || strings.Contains(out, "style=") {
		t.Fatalf("pasted formatting was not sanitized: %s", out)
	}
}
