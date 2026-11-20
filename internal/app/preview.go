package app

import "strings"

// previewRuneLimit bounds every one-line excerpt the server hands to a list
// surface (inbox rows, thread reply previews). Full bodies never travel there.
const previewRuneLimit = 120

func previewText(body string) string {
	text := strings.TrimSpace(body)
	if r := []rune(text); len(r) > previewRuneLimit {
		text = string(r[:previewRuneLimit]) + "…"
	}
	return text
}
