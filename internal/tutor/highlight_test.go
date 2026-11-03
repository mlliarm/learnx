package tutor

import (
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

func TestTerminalMarkdown(t *testing.T) {
	md := "Intro.\n\n```http\nPUT collections/books\n{\"vectors\": {\"size\": 4}}\n```\n\n" +
		"```console\n# setup\nPOST auth-events/_bulk\n{\"index\": {}}\n```\n\n" +
		"```http\nGET /index.html HTTP/1.1\nHost: example.com\n```\n\n" +
		"```json\n{\"points\": []}\n```\n\n" +
		"```sql\nSELECT 1\n```\n"
	got := TerminalMarkdown(md)
	want := "Intro.\n\n```learnx-console\nPUT collections/books\n{\"vectors\": {\"size\": 4}}\n```\n\n" +
		"```learnx-console\n# setup\nPOST auth-events/_bulk\n{\"index\": {}}\n```\n\n" +
		"```http\nGET /index.html HTTP/1.1\nHost: example.com\n```\n\n" +
		"```json\n{\"points\": []}\n```\n\n" +
		"```sql\nSELECT 1\n```\n"
	if got != want {
		t.Errorf("TerminalMarkdown:\n%s\nwant:\n%s", got, want)
	}
}

func TestConsoleLexer(t *testing.T) {
	l := lexers.Get(consoleLang)
	if l == nil {
		t.Fatal("console lexer not registered")
	}
	code := "PUT collections/books\n{\"vectors\": {\"size\": 4, \"distance\": \"Cosine\"}}\n\n" +
		"POST collections/books/points/query\n{\n  \"query\": [1.0, 0.0],\n  \"with_payload\": true\n}\n"
	it, err := l.Tokenise(nil, code)
	if err != nil {
		t.Fatal(err)
	}
	var keywords []string
	var strs int
	for _, tok := range it.Tokens() {
		switch {
		case tok.Type == chroma.Error:
			t.Errorf("error token %q", tok.Value)
		case tok.Type == chroma.Keyword:
			keywords = append(keywords, tok.Value)
		case tok.Type.InCategory(chroma.Literal) || tok.Type == chroma.NameTag:
			strs++
		}
	}
	if strings.Join(keywords, " ") != "PUT POST" || strs == 0 {
		t.Errorf("keywords = %v, literals = %d: the methods or the JSON body weren't highlighted", keywords, strs)
	}
}
