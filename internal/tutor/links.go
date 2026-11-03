package tutor

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/glamour"
)

// RenderTerminal renders Markdown with r for the terminal. Links become
// terminal hyperlinks (OSC 8) on their text instead of glamour's "text URL",
// and bare URLs are linked once instead of printed twice.
func RenderTerminal(r *glamour.TermRenderer, md string) (string, error) {
	md = TerminalMarkdown(md)
	marked, urls := markLinks(md)
	if len(urls) == 0 {
		return r.Render(md)
	}
	out, err := r.Render(marked)
	if err != nil {
		return "", err
	}
	if strings.Count(out, linkMark) != 2*len(urls) {
		// The renderer dropped or split a link: show the URLs rather than
		// link the wrong text.
		return r.Render(md)
	}
	return hyperlink(out, urls), nil
}

// linkMark brackets the text of each link through rendering. A zero-width
// space: it survives glamour, and every width function in its pipeline
// counts it as zero columns, so wrapping and table layout are unaffected.
const linkMark = "\u200b"

var (
	// anyLink matches, in one pass so a link's text isn't searched again:
	// [text](url) or [text](url "title") with one level of parentheses
	// allowed in the URL, a leading "!" making it an image (groups 1-3); an
	// autolink <url> (group 4); or a URL in running text.
	anyLink = regexp.MustCompile(`(!?)\[([^\[\]]*)\]\(\s*<?((?:[^\s()<>]|\([^\s()]*\))+)>?(?:\s+"[^"]*")?\s*\)` +
		`|<(https?://[^\s<>]+)>` +
		"|https?://[^\\s<>()\\[\\]`\x00]+")
	codeSpan  = regexp.MustCompile("(`+)[^`](?:.*?[^`])?(`+)")
	codeToken = regexp.MustCompile("\x00(\\d+)\x00")
)

// markLinks rewrites every link outside code as [mark text mark](#), which
// glamour shows as styled text without a URL, and returns the URLs in order.
func markLinks(md string) (string, []string) {
	var urls []string
	lines := strings.Split(strings.ReplaceAll(md, linkMark, ""), "\n")
	for i := 0; i < len(lines); i++ {
		if m := fenceLine.FindStringSubmatch(lines[i]); m != nil {
			for i++; i < len(lines) && !closesFence(lines[i], m[2]); i++ {
			}
			continue
		}
		lines[i] = markLine(lines[i], &urls)
	}
	return strings.Join(lines, "\n"), urls
}

func markLine(line string, urls *[]string) string {
	// Hide code spans so links inside them stay literal; a link's text may
	// still contain one.
	var spans []string
	line = codeSpan.ReplaceAllStringFunc(line, func(s string) string {
		m := codeSpan.FindStringSubmatch(s)
		if len(m[1]) != len(m[2]) {
			return s
		}
		spans = append(spans, s)
		return fmt.Sprintf("\x00%d\x00", len(spans)-1)
	})

	link := func(text, url string) string {
		*urls = append(*urls, url)
		return "[" + linkMark + text + linkMark + "](#)"
	}
	line = anyLink.ReplaceAllStringFunc(line, func(s string) string {
		m := anyLink.FindStringSubmatch(s)
		switch {
		case m[4] != "":
			return link(m[4], m[4])
		case strings.HasPrefix(s, "http"):
			// Sentence punctuation after a URL isn't part of it.
			url := strings.TrimRight(s, ".,;:!?'\"*_")
			return link(url, url) + s[len(url):]
		case m[1] == "!" || strings.TrimSpace(m[2]) == "" || strings.HasPrefix(m[3], "#"):
			return s
		}
		return link(m[2], m[3])
	})

	return codeToken.ReplaceAllStringFunc(line, func(s string) string {
		var n int
		fmt.Sscanf(strings.Trim(s, "\x00"), "%d", &n)
		return spans[n]
	})
}

// hyperlink turns each pair of marks in rendered output into an OSC 8
// hyperlink to the matching URL. A link that wraps is closed at the end of
// each line and reopened at its next visible character, so neither the
// renderer's padding nor the indent of the next line is linked; the shared id
// lets terminals highlight the pieces together.
func hyperlink(out string, urls []string) string {
	open := func(k int) string { return fmt.Sprintf("\x1b]8;id=learnx-%d;%s\x1b\\", k, urls[k]) }
	const closeSeq = "\x1b]8;;\x1b\\"

	var sb strings.Builder
	k, inLink := 0, false
	for li, line := range strings.Split(out, "\n") {
		if li > 0 {
			sb.WriteByte('\n')
		}
		var lb strings.Builder
		reopen := inLink
		closeAt := -1 // where the open link's text ends on this line
		for i := 0; i < len(line); {
			if line[i] == 0x1b {
				n := escapeLen(line[i:])
				lb.WriteString(line[i : i+n])
				i += n
				continue
			}
			if strings.HasPrefix(line[i:], linkMark) {
				i += len(linkMark)
				if !inLink {
					lb.WriteString(open(k))
					inLink, reopen, closeAt = true, false, lb.Len()
				} else {
					if !reopen {
						lb.WriteString(closeSeq)
					}
					inLink, reopen, closeAt = false, false, -1
					k++
				}
				continue
			}
			c := line[i]
			if inLink && c != ' ' {
				if reopen {
					lb.WriteString(open(k))
					reopen = false
				}
			}
			n := 1
			for i+n < len(line) && line[i+n]&0xc0 == 0x80 { // rest of a UTF-8 rune
				n++
			}
			lb.WriteString(line[i : i+n])
			i += n
			if inLink && c != ' ' {
				closeAt = lb.Len()
			}
		}
		s := lb.String()
		if inLink && !reopen {
			s = s[:closeAt] + closeSeq + s[closeAt:]
		}
		sb.WriteString(s)
	}
	return sb.String()
}

// escapeLen is the length of the escape sequence at the start of s: CSI
// (ESC [ ... final byte), OSC (ESC ] ... BEL or ESC \) or a two-byte escape.
func escapeLen(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[':
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
	case ']':
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
	default:
		return 2
	}
	return len(s)
}
