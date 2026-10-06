package store

import (
	"bytes"
	"fmt"
	stdhtml "html"
	"net/url"
	"regexp"
	"strings"

	nethtml "golang.org/x/net/html"
)

var contentImageURL = regexp.MustCompile(`^/api/v1/content-images/[1-9][0-9]*$`)
var contentAllowedTags = map[string]bool{"p": true, "br": true, "strong": true, "em": true, "u": true, "ul": true, "ol": true, "li": true, "h2": true, "h3": true, "blockquote": true, "code": true, "pre": true, "a": true, "img": true}

// SanitizeContentHTML drops executable markup and restricts links/images to safe URLs.
func SanitizeContentHTML(input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", nil
	}
	doc, err := nethtml.Parse(strings.NewReader(input))
	if err != nil {
		return "", fmt.Errorf("invalid html")
	}
	var out bytes.Buffer
	var render func(*nethtml.Node)
	render = func(n *nethtml.Node) {
		if n.Type == nethtml.TextNode {
			out.WriteString(stdhtml.EscapeString(n.Data))
			return
		}
		if n.Type != nethtml.ElementNode {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				render(c)
			}
			return
		}
		if !contentAllowedTags[n.Data] {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				render(c)
			}
			return
		}
		out.WriteByte('<')
		out.WriteString(n.Data)
		for _, a := range n.Attr {
			name := strings.ToLower(a.Key)
			value := strings.TrimSpace(a.Val)
			switch name {
			case "alt", "title":
				if n.Data == "img" || n.Data == "a" {
					fmt.Fprintf(&out, ` %s="%s"`, name, stdhtml.EscapeString(value))
				}
			case "href":
				if n.Data == "a" && safeContentLink(value) {
					fmt.Fprintf(&out, ` href="%s" rel="nofollow noopener"`, stdhtml.EscapeString(value))
				}
			case "src":
				if n.Data == "img" && contentImageURL.MatchString(value) {
					fmt.Fprintf(&out, ` src="%s"`, stdhtml.EscapeString(value))
				}
			}
		}
		out.WriteByte('>')
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			render(c)
		}
		if n.Data != "br" && n.Data != "img" {
			out.WriteString("</" + n.Data + ">")
		}
	}
	for c := doc.FirstChild; c != nil; c = c.NextSibling {
		render(c)
	}
	result := strings.TrimSpace(out.String())
	if len(result) > 120000 {
		return "", fmt.Errorf("html too large")
	}
	return result, nil
}

func safeContentLink(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.User != nil {
		return false
	}
	if u.IsAbs() {
		return u.Scheme == "https" || u.Scheme == "http"
	}
	return strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//")
}
