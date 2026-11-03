package tutor

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/glamour"
)

func TestMarkLinks(t *testing.T) {
	m := linkMark
	md := "See [data streams](https://x.test/ds) and [`keyword` type](https://x.test/kw \"Keyword\").\n" +
		"Bare https://x.test/bare. and <https://x.test/auto>; [https://x.test/self](https://x.test/self).\n" +
		"Wiki [page](https://x.test/a_(b)), image ![alt](https://x.test/i.png), anchor [up](#top).\n" +
		"Code `[no](https://x.test/no)` and ``https://x.test/code``.\n" +
		"```\n[fenced](https://x.test/fenced) https://x.test/fenced\n```\n"
	got, urls := markLinks(md)
	want := "See [" + m + "data streams" + m + "](#) and [" + m + "`keyword` type" + m + "](#).\n" +
		"Bare [" + m + "https://x.test/bare" + m + "](#). and [" + m + "https://x.test/auto" + m + "](#); [" + m + "https://x.test/self" + m + "](#).\n" +
		"Wiki [" + m + "page" + m + "](#), image ![alt](https://x.test/i.png), anchor [up](#top).\n" +
		"Code `[no](https://x.test/no)` and ``https://x.test/code``.\n" +
		"```\n[fenced](https://x.test/fenced) https://x.test/fenced\n```\n"
	if got != want {
		t.Errorf("markLinks:\n%q\nwant:\n%q", got, want)
	}
	wantURLs := []string{"https://x.test/ds", "https://x.test/kw", "https://x.test/bare", "https://x.test/auto", "https://x.test/self", "https://x.test/a_(b)"}
	if !slices.Equal(urls, wantURLs) {
		t.Errorf("urls = %q, want %q", urls, wantURLs)
	}
}

// osc8 shows OSC 8 hyperlinks as <url|text> and drops other escapes.
var (
	osc8Open  = regexp.MustCompile("\x1b\\]8;id=[^;]*;([^\x1b]+)\x1b\\\\")
	osc8Close = regexp.MustCompile("\x1b\\]8;;\x1b\\\\")
	csi       = regexp.MustCompile("\x1b\\[[0-9;]*m")
)

func osc8(s string) string {
	s = osc8Open.ReplaceAllString(s, "<$1|")
	s = osc8Close.ReplaceAllString(s, ">")
	return csi.ReplaceAllString(s, "")
}

func TestRenderTerminal(t *testing.T) {
	for _, style := range []string{"notty", "dark"} {
		r, err := glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(40))
		if err != nil {
			t.Fatal(err)
		}
		md := "Read about [data streams in Elasticsearch](https://x.test/ds) before https://x.test/bare.\n\n" +
			"| a | b |\n|---|---|\n| [x](https://x.test/x) | y |\n"
		out, err := RenderTerminal(r, md)
		if err != nil {
			t.Fatal(err)
		}
		got := osc8(out)
		var lines []string
		for _, l := range strings.Split(got, "\n") {
			lines = append(lines, strings.TrimSpace(l))
		}
		// The wrapped link is closed before the padding and reopened after
		// the indent.
		for _, want := range []string{
			"Read about <https://x.test/ds|data streams in>",
			"<https://x.test/ds|Elasticsearch> before",
			"<https://x.test/bare|https://x.test/bare>.",
		} {
			if !slices.Contains(lines, want) {
				t.Errorf("%s: output lacks line %q:\n%s", style, want, got)
			}
		}
		if !strings.Contains(got, "<https://x.test/x|x>") {
			t.Errorf("%s: table link missing:\n%s", style, got)
		}
		if strings.Contains(got, linkMark) || strings.Count(got, "https://x.test/bare") != 2 {
			t.Errorf("%s: marks left or URL printed as text:\n%q", style, got)
		}
	}
}
