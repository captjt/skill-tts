// Package textprep turns agent output (usually Markdown) into speakable text
// and splits long text into provider-sized chunks.
package textprep

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	fencedCode = regexp.MustCompile("(?ms)^[ \t]*(```|~~~).*?^[ \t]*(```|~~~)[ \t]*$")
	image      = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	link       = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	bareURL    = regexp.MustCompile(`<?https?://[^\s<>)\]]+>?`)
	inlineCode = regexp.MustCompile("`([^`\n]+)`")
	bold       = regexp.MustCompile(`(\*\*|__)(\S(?:.*?\S)?)(\*\*|__)`)
	italic     = regexp.MustCompile(`(^|[\s(])\*(\S(?:[^*\n]*\S)?)\*`)
	heading    = regexp.MustCompile(`(?m)^[ \t]*#{1,6}[ \t]+`)
	quote      = regexp.MustCompile(`(?m)^[ \t]*>[ \t]?`)
	bullet     = regexp.MustCompile(`(?m)^[ \t]*[-*+][ \t]+`)
	rule       = regexp.MustCompile(`(?m)^[ \t]*([-*_][ \t]*){3,}$`)
	tableSep   = regexp.MustCompile(`(?m)^[ \t]*\|?[ \t]*:?-{3,}:?[ \t]*(\|[ \t]*:?-{3,}:?[ \t]*)*\|?[ \t]*$`)
	tableRow   = regexp.MustCompile(`(?m)^[ \t]*\|(.*)\|[ \t]*$`)
	blankLines = regexp.MustCompile(`\n{3,}`)
	spaces     = regexp.MustCompile(`[ \t]+`)
)

// Clean strips Markdown syntax that sounds bad when read aloud: code blocks
// are dropped, links keep their text, URLs are reduced to their host.
func Clean(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = fencedCode.ReplaceAllString(s, "")
	s = image.ReplaceAllString(s, "$1")
	s = link.ReplaceAllString(s, "$1")
	s = bareURL.ReplaceAllStringFunc(s, func(u string) string {
		if p, err := url.Parse(strings.Trim(u, "<>")); err == nil && p.Host != "" {
			return strings.TrimPrefix(p.Host, "www.")
		}
		return u
	})
	s = inlineCode.ReplaceAllString(s, "$1")
	s = bold.ReplaceAllString(s, "$2")
	s = italic.ReplaceAllString(s, "$1$2")
	s = rule.ReplaceAllString(s, "")
	s = tableSep.ReplaceAllString(s, "")
	s = tableRow.ReplaceAllStringFunc(s, func(row string) string {
		cells := strings.Split(strings.Trim(strings.TrimSpace(row), "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		return strings.Join(cells, ", ") + "."
	})
	s = heading.ReplaceAllString(s, "")
	s = quote.ReplaceAllString(s, "")
	s = bullet.ReplaceAllString(s, "")
	s = spaces.ReplaceAllString(s, " ")
	s = blankLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// Chunk splits text into pieces of at most max bytes, preferring paragraph,
// then sentence, then clause, then word boundaries. Short text is returned as
// a single chunk so the provider sees the full context for prosody.
func Chunk(text string, max int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if len(text) <= max {
		return []string{text}
	}
	var chunks []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			chunks = append(chunks, s)
		}
		cur.Reset()
	}
	for _, piece := range pieces(text, max) {
		if cur.Len() > 0 && cur.Len()+len(piece) > max {
			flush()
		}
		cur.WriteString(piece)
	}
	flush()
	return chunks
}

// ChunkFastStart is Chunk, except the first chunk is kept to about first bytes
// by ending it early at a sentence or clause boundary. Providers that render a
// whole chunk before streaming any audio start speaking sooner this way, and
// the rest is fetched while the opening plays. It never cuts mid-clause.
func ChunkFastStart(text string, first, max int) []string {
	text = strings.TrimSpace(text)
	if first <= 0 || len(text) <= first {
		return Chunk(text, max)
	}
	var head strings.Builder
	rest := text
	for _, sent := range splitAfter(text, sentenceEnd) {
		for _, clause := range splitAfter(sent, clauseEnd) {
			if head.Len() > 0 && head.Len()+len(clause) > first {
				goto done
			}
			head.WriteString(clause)
			rest = rest[len(clause):]
		}
		if head.Len() >= first/2 { // a whole sentence is a good place to stop
			break
		}
	}
done:
	if strings.TrimSpace(rest) == "" || head.Len() > max {
		return Chunk(text, max)
	}
	return append([]string{strings.TrimSpace(head.String())}, Chunk(rest, max)...)
}

// pieces splits text into units no longer than max, each keeping its trailing
// separator so that joining them reproduces the input.
func pieces(text string, max int) []string {
	var out []string
	for _, para := range splitAfter(text, paraEnd) {
		if len(para) <= max {
			out = append(out, para)
			continue
		}
		for _, sent := range splitAfter(para, sentenceEnd) {
			if len(sent) <= max {
				out = append(out, sent)
				continue
			}
			for _, clause := range splitAfter(sent, clauseEnd) {
				if len(clause) <= max {
					out = append(out, clause)
					continue
				}
				for _, word := range splitAfter(clause, wordEnd) {
					out = append(out, hardSplit(word, max)...)
				}
			}
		}
	}
	return out
}

var (
	paraEnd     = regexp.MustCompile(`\n\s*\n`)
	sentenceEnd = regexp.MustCompile(`[.!?…。！？]+["'”’)\]]*\s+|\n`)
	clauseEnd   = regexp.MustCompile(`[,;:—–]\s+`)
	wordEnd     = regexp.MustCompile(`\s+`)
)

func splitAfter(s string, sep *regexp.Regexp) []string {
	var out []string
	last := 0
	for _, m := range sep.FindAllStringIndex(s, -1) {
		out = append(out, s[last:m[1]])
		last = m[1]
	}
	if last < len(s) {
		out = append(out, s[last:])
	}
	return out
}

// hardSplit cuts s into max-byte pieces without splitting UTF-8 sequences.
func hardSplit(s string, max int) []string {
	var out []string
	for len(s) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			cut = max
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return append(out, s)
}
