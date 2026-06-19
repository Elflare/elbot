package telegram

import (
	"strings"
	"testing"
)

func TestTelegramHTMLFromMarkdownBlocks(t *testing.T) {
	input := "# 标题\n\n> 引用 **重点**\n\n---\n\n| A | B |\n|---|---|\n| 1 | 2 |"
	out := telegramHTMLFromMarkdown(input)
	for _, want := range []string{"<b>标题</b>", "<blockquote>引用 <b>重点</b></blockquote>", "────────", "<pre>", "A  B"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestTelegramHTMLFromMarkdownCode(t *testing.T) {
	input := "```go\nfmt.Println(\"<hi>\")\n```\ninline `x < y`"
	out := telegramHTMLFromMarkdown(input)
	if !strings.Contains(out, `<pre><code class="language-go">fmt.Println(&#34;&lt;hi&gt;&#34;)</code></pre>`) {
		t.Fatalf("code block output:\n%s", out)
	}
	if !strings.Contains(out, "inline <code>x &lt; y</code>") {
		t.Fatalf("inline code output:\n%s", out)
	}
}
