package docs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mlliarm/learnx/internal/pack"
)

// MCPSource talks to a remote docs MCP server over Streamable HTTP.
type MCPSource struct {
	cfg     pack.MCPDocs
	session *mcp.ClientSession
}

func OpenMCP(ctx context.Context, cfg pack.MCPDocs) (*MCPSource, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "learnx", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: cfg.Endpoint}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", cfg.Endpoint, err)
	}
	return &MCPSource{cfg: cfg, session: session}, nil
}

func (s *MCPSource) Close() error { return s.session.Close() }

// Tools lists the tools the server exposes; later these get handed to the LLM.
func (s *MCPSource) Tools(ctx context.Context) ([]*mcp.Tool, error) {
	res, err := s.session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	return res.Tools, nil
}

// CallTool invokes any server tool with raw JSON arguments and returns its
// text output. Tool-level failures come back as an error.
func (s *MCPSource) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	res, err := s.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", fmt.Errorf("call %s: %w", name, err)
	}
	if res.IsError {
		return "", fmt.Errorf("tool %s failed: %s", name, textOf(res))
	}
	return textOf(res), nil
}

func (s *MCPSource) Search(ctx context.Context, query string) ([]Result, error) {
	res, err := s.call(ctx, s.cfg.SearchTool, query)
	if err != nil {
		return nil, err
	}
	if docs, ok := parseRAGDocuments(res.StructuredContent); ok {
		return docs, nil
	}
	if docs, ok := parseSearchResults(textOf(res), s.cfg.BaseURL); ok {
		return docs, nil
	}
	// Server without structured output: treat the whole text as one result.
	return []Result{{Text: textOf(res)}}, nil
}

func (s *MCPSource) Ask(ctx context.Context, question string) (string, error) {
	if s.cfg.AskTool == "" {
		return "", ErrUnsupported
	}
	res, err := s.call(ctx, s.cfg.AskTool, question)
	if err != nil {
		return "", err
	}
	var out struct {
		Result string `json:"result"`
	}
	if b, err := json.Marshal(res.StructuredContent); err == nil && json.Unmarshal(b, &out) == nil && out.Result != "" {
		return out.Result, nil
	}
	return textOf(res), nil
}

func (s *MCPSource) call(ctx context.Context, tool, query string) (*mcp.CallToolResult, error) {
	if tool == "" {
		return nil, ErrUnsupported
	}
	res, err := s.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      tool,
		Arguments: map[string]any{"query": query},
	})
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", tool, err)
	}
	if res.IsError {
		return nil, fmt.Errorf("tool %s failed: %s", tool, textOf(res))
	}
	return res, nil
}

// parseRAGDocuments decodes the Inkeep-style {content: [{title, url, source: {content: [{text}]}}]} shape.
func parseRAGDocuments(v any) ([]Result, bool) {
	if v == nil {
		return nil, false
	}
	var payload struct {
		Content []struct {
			Title  string `json:"title"`
			URL    string `json:"url"`
			Source struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"source"`
		} `json:"content"`
	}
	b, err := json.Marshal(v)
	if err != nil || json.Unmarshal(b, &payload) != nil || len(payload.Content) == 0 {
		return nil, false
	}
	results := make([]Result, 0, len(payload.Content))
	for _, d := range payload.Content {
		var sb strings.Builder
		for _, c := range d.Source.Content {
			sb.WriteString(c.Text)
		}
		results = append(results, Result{Title: d.Title, URL: d.URL, Text: strings.TrimSpace(sb.String())})
	}
	return results, true
}

// parseSearchResults decodes the Elastic docs-mcp shape:
// {"results": [{title, url, description, aiShortSummary}]}, with url often
// relative to base.
func parseSearchResults(text, base string) ([]Result, bool) {
	var payload struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			Summary     string `json:"aiShortSummary"`
		} `json:"results"`
	}
	if json.Unmarshal([]byte(text), &payload) != nil || len(payload.Results) == 0 {
		return nil, false
	}
	results := make([]Result, 0, len(payload.Results))
	for _, r := range payload.Results {
		body := r.Description
		if r.Summary != "" && r.Summary != r.Description {
			body = strings.TrimSpace(r.Summary + "\n" + r.Description)
		}
		results = append(results, Result{Title: r.Title, URL: Absolute(base, r.URL), Text: body})
	}
	return results, true
}

// Absolute resolves a site-relative URL ("/docs/x") against base.
func Absolute(base, u string) string {
	if base == "" || !strings.HasPrefix(u, "/") || strings.HasPrefix(u, "//") {
		return u
	}
	return strings.TrimSuffix(base, "/") + u
}

func textOf(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}
