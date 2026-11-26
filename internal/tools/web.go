package tools

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Web search (DuckDuckGo's HTML page: no API key) and page fetching.

const userAgent = "Mozilla/5.0 (X11; Linux x86_64) openLLinuxM"

var webClient = &http.Client{Timeout: 25 * time.Second}

func httpGet(ctx context.Context, u string, limit int64) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json;q=0.9,*/*;q=0.5")
	resp, err := webClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return string(b), resp.Header.Get("Content-Type"), err
}

var (
	ddgResult  = regexp.MustCompile(`(?s)<a[^>]+class="result__a"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnippet = regexp.MustCompile(`(?s)class="result__snippet"[^>]*>(.*?)</a>`)
	anyTag     = regexp.MustCompile(`(?s)<[^>]+>`)
)

// webSearch returns up to n results as "title\nurl\nsnippet" blocks.
func webSearch(ctx context.Context, query string, n int) (string, error) {
	page, _, err := httpGet(ctx, "https://html.duckduckgo.com/html/?q="+url.QueryEscape(query), 2<<20)
	if err != nil {
		return "", err
	}
	links := ddgResult.FindAllStringSubmatch(page, -1)
	snips := ddgSnippet.FindAllStringSubmatch(page, -1)
	var b strings.Builder
	k := 0
	for i, l := range links {
		if k == n {
			break
		}
		href := html.UnescapeString(l[1])
		// DuckDuckGo wraps links: //duckduckgo.com/l/?uddg=<real url>
		if u, err := url.Parse(href); err == nil {
			if real := u.Query().Get("uddg"); real != "" {
				href = real
			}
		}
		if strings.Contains(href, "duckduckgo.com/y.js") { // ads
			continue
		}
		k++
		title := cleanText(l[2])
		snip := ""
		if i < len(snips) {
			snip = cleanText(snips[i][1])
		}
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", k, title, href, snip)
	}
	if k == 0 {
		return "no results", nil
	}
	return b.String(), nil
}

func cleanText(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(anyTag.ReplaceAllString(s, ""))), " ")
}

var (
	dropBlocks = regexp.MustCompile(`(?is)<(script|style|noscript|svg|head|nav|footer|form)\b.*?</(script|style|noscript|svg|head|nav|footer|form)>`)
	blockTags  = regexp.MustCompile(`(?i)<\s*/?\s*(p|div|br|li|h[1-6]|tr|section|article|pre|blockquote|table|ul|ol)\b[^>]*>`)
	comments   = regexp.MustCompile(`(?s)<!--.*?-->`)
	spaces     = regexp.MustCompile(`[ \t\r\f\v]+`)
	blankLines = regexp.MustCompile(`\n\s*\n\s*\n+`)
)

// webFetch returns a page as text: HTML is reduced to its readable text, other text as is.
func webFetch(ctx context.Context, u string) (string, error) {
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = "https://" + u
	}
	body, ctype, err := httpGet(ctx, u, 4<<20)
	if err != nil {
		return "", err
	}
	if !strings.Contains(ctype, "html") && !strings.Contains(strings.ToLower(body[:min(len(body), 512)]), "<html") {
		return body, nil
	}
	t := comments.ReplaceAllString(body, "")
	t = dropBlocks.ReplaceAllString(t, "")
	t = blockTags.ReplaceAllString(t, "\n")
	t = anyTag.ReplaceAllString(t, "")
	t = html.UnescapeString(t)
	t = spaces.ReplaceAllString(t, " ")
	lines := strings.Split(t, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	t = blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(t), nil
}
