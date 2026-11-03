package tutor

import (
	"regexp"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// consoleLang names a chroma lexer for console requests (a request line such
// as "PUT auth-events", then a JSON body), as typed in Kibana Dev Tools and
// used by the Elasticsearch sandbox. Chroma has none: its "console" is a
// shell session and its "http" wants a full HTTP message, so it marks such
// blocks as errors.
const consoleLang = "learnx-console"

func init() {
	lexers.Register(chroma.MustNewLexer(
		&chroma.Config{Name: "Console requests", Aliases: []string{consoleLang}, EnsureNL: true},
		func() chroma.Rules {
			return chroma.Rules{
				"root": {
					{Pattern: `^(GET|PUT|POST|DELETE|HEAD|PATCH)([ \t]+)(\S+)([ \t]*\n)`,
						Type: chroma.ByGroups(chroma.Keyword, chroma.Text, chroma.NameFunction, chroma.Text)},
					{Pattern: `^[ \t]*(#|//)[^\n]*\n`, Type: chroma.CommentSingle},
					// A body: every line up to the next request line or comment,
					// lexed in one piece because the JSON lexer keeps state.
					{Pattern: `(?:(?![ \t]*(?:(?:GET|PUT|POST|DELETE|HEAD|PATCH)[ \t]+\S+[ \t]*\n|#|//))[^\n]*\n)+`,
						Type: chroma.Using("JSON")},
				},
			}
		},
	))
}

var (
	fenceLine   = regexp.MustCompile("^(\\s*)(```+|~~~+)\\s*([\\w+-]*)\\s*$")
	requestLine = regexp.MustCompile(`^(GET|PUT|POST|DELETE|HEAD|PATCH)\s+\S+\s*$`)
)

// TerminalMarkdown prepares Markdown for the terminal renderer: code blocks
// that hold console requests, whatever their fence says, are highlighted as
// such.
func TerminalMarkdown(md string) string {
	lines := strings.Split(md, "\n")
	for i := 0; i < len(lines); i++ {
		m := fenceLine.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		end := i + 1
		for end < len(lines) && !closesFence(lines[end], m[2]) {
			end++
		}
		if isConsole(lines[i+1 : end]) {
			lines[i] = m[1] + m[2] + consoleLang
		}
		i = end
	}
	return strings.Join(lines, "\n")
}

func closesFence(line, fence string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == ""
}

// isConsole: the first line that isn't blank or a comment is a request line.
func isConsole(code []string) bool {
	for _, l := range code {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") {
			continue
		}
		return requestLine.MatchString(t)
	}
	return false
}
