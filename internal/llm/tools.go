package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/mlliarm/learnx/internal/docs"
)

// DocsTools exposes a docs source to Claude. An MCP source's tools are
// mirrored one-to-one; any other source gets a synthesized search tool.
func DocsTools(ctx context.Context, src docs.Source, status io.Writer) ([]anthropic.BetaTool, error) {
	m, ok := src.(*docs.MCPSource)
	if !ok {
		return []anthropic.BetaTool{&searchTool{src: src, status: status}}, nil
	}
	mcpTools, err := m.Tools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list docs tools: %w", err)
	}
	tools := make([]anthropic.BetaTool, 0, len(mcpTools))
	for _, t := range mcpTools {
		schema, err := toMap(t.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("tool %s schema: %w", t.Name, err)
		}
		tools = append(tools, &mcpTool{
			src:    m,
			name:   t.Name,
			desc:   t.Description,
			schema: inputSchema(schema),
			status: status,
		})
	}
	return tools, nil
}

type mcpTool struct {
	src    *docs.MCPSource
	name   string
	desc   string
	schema anthropic.BetaToolInputSchemaParam
	status io.Writer
}

func (t *mcpTool) Name() string                                    { return t.name }
func (t *mcpTool) Description() string                             { return t.desc }
func (t *mcpTool) InputSchema() anthropic.BetaToolInputSchemaParam { return t.schema }

func (t *mcpTool) Execute(ctx context.Context, input json.RawMessage) ([]anthropic.BetaToolResultBlockParamContentUnion, error) {
	logCall(t.status, t.name, input)
	out, err := t.src.CallTool(ctx, t.name, input)
	if err != nil {
		if t.status != nil {
			fmt.Fprintf(t.status, "    ✗ %v\n", err)
		}
		return nil, err
	}
	return textResult(out), nil
}

type searchTool struct {
	src    docs.Source
	status io.Writer
}

func (t *searchTool) Name() string { return "search_docs" }
func (t *searchTool) Description() string {
	return "Search the official documentation. Returns page extracts with URLs."
}
func (t *searchTool) InputSchema() anthropic.BetaToolInputSchemaParam {
	return anthropic.BetaToolInputSchema(map[string]any{
		"type":       "object",
		"properties": map[string]any{"query": map[string]any{"type": "string"}},
		"required":   []string{"query"},
	})
}

func (t *searchTool) Execute(ctx context.Context, input json.RawMessage) ([]anthropic.BetaToolResultBlockParamContentUnion, error) {
	logCall(t.status, t.Name(), input)
	var in struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(input, &in); err != nil || in.Query == "" {
		return nil, fmt.Errorf("invalid input: want {\"query\": string}")
	}
	results, err := t.src.Search(ctx, in.Query)
	if err != nil {
		return nil, err
	}
	var sb strings.Builder
	for _, r := range results {
		fmt.Fprintf(&sb, "## %s\nURL: %s\n\n%s\n\n", r.Title, r.URL, r.Text)
	}
	return textResult(sb.String()), nil
}

func textResult(s string) []anthropic.BetaToolResultBlockParamContentUnion {
	return []anthropic.BetaToolResultBlockParamContentUnion{{OfText: &anthropic.BetaTextBlockParam{Text: s}}}
}

func logCall(w io.Writer, name string, input json.RawMessage) {
	if w == nil {
		return
	}
	var in struct {
		Query string `json:"query"`
	}
	if json.Unmarshal(input, &in) == nil && in.Query != "" {
		fmt.Fprintf(w, "  ↳ %s: %s\n", name, in.Query)
		return
	}
	fmt.Fprintf(w, "  ↳ %s %s\n", name, input)
}

// inputSchema passes an MCP tool's JSON schema through unchanged. The SDK's
// BetaToolInputSchema rewrites schemas for strict structured outputs and
// drops every property of one it can't rewrite (e.g. "type": ["string",
// "null"]), which leaves the model unable to call the tool.
func inputSchema(m map[string]any) anthropic.BetaToolInputSchemaParam {
	p := anthropic.BetaToolInputSchemaParam{Properties: m["properties"], ExtraFields: map[string]any{}}
	if req, ok := m["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				p.Required = append(p.Required, s)
			}
		}
	}
	for k, v := range m {
		if k != "type" && k != "properties" && k != "required" {
			p.ExtraFields[k] = v
		}
	}
	return p
}

func toMap(v any) (map[string]any, error) {
	if m, ok := v.(map[string]any); ok {
		return m, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(b, &m)
}
