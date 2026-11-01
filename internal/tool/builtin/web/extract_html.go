// 清理直抓的 HTML 为纯文本，并保留链接地址。
package web

import (
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

func extractSimpleHTMLText(raw string, baseURL *url.URL) (string, string) {
	tokenizer := html.NewTokenizer(strings.NewReader(raw))
	var title, text strings.Builder
	inTitle := false
	skipTag := ""
	href := ""
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			if href != "" {
				text.WriteString(" (" + href + ")")
			}
			return strings.TrimSpace(title.String()), strings.Join(strings.Fields(text.String()), " ")
		case html.TextToken:
			if skipTag != "" {
				continue
			}
			token := tokenizer.Text()
			if inTitle {
				title.Write(token)
			}
			text.Write(token)
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			token := tokenizer.Token()
			if skipTag != "" {
				if token.Type == html.EndTagToken && token.Data == skipTag {
					skipTag = ""
				}
				continue
			}
			text.WriteByte(' ')
			switch token.Data {
			case "script", "style", "noscript":
				if token.Type == html.StartTagToken {
					skipTag = token.Data
				}
			case "title":
				inTitle = token.Type == html.StartTagToken
			case "a":
				if token.Type == html.StartTagToken {
					href = ""
					for _, attr := range token.Attr {
						if attr.Key == "href" {
							if parsed, err := url.Parse(attr.Val); err == nil {
								href = baseURL.ResolveReference(parsed).String()
							}
							break
						}
					}
				} else if token.Type == html.EndTagToken && href != "" {
					text.WriteString("(" + href + ") ")
					href = ""
				}
			}
		case html.CommentToken, html.DoctypeToken:
			text.WriteByte(' ')
		}
	}
}
