package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/invopop/jsonschema"
)

// OpenRouterModel is the default when only an OpenRouter key is configured.
const OpenRouterModel = "openai/gpt-6-luna"

const openRouterURL = "https://openrouter.ai/api/v1"

// isOpenRouter reports whether a model ID names an OpenRouter model. Their
// IDs are always "vendor/model"; Claude model IDs never contain a slash.
func isOpenRouter(model string) bool { return strings.Contains(model, "/") }

// orMessage is a chat message we send. Assistant turns are echoed back raw so
// tool calls and reasoning details survive unchanged.
type orMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type orResponse struct {
	Choices []struct {
		Message      json.RawMessage `json:"message"`
		FinishReason string          `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		Cost             float64 `json:"cost"`
	} `json:"usage"`
	Error *orError `json:"error"`
}

type orError struct {
	Code    any    `json:"code"`
	Message string `json:"message"`
}

type orAssistant struct {
	Content   string `json:"content"`
	Refusal   string `json:"refusal"`
	ToolCalls []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// runOpenRouter is Run's tool loop over OpenRouter's chat completions API.
func (c *Client) runOpenRouter(ctx context.Context, req Request) (string, error) {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		return "", fmt.Errorf("model %s runs on OpenRouter; set OPENROUTER_API_KEY", c.Model)
	}
	base := os.Getenv("OPENROUTER_BASE_URL")
	if base == "" {
		base = openRouterURL
	}

	body := map[string]any{
		"model":      c.Model,
		"max_tokens": 64000,
		"usage":      map[string]any{"include": true},
	}
	if req.Effort != "" {
		body["reasoning"] = map[string]any{"effort": orEffort(req.Effort)}
	}
	byName := map[string]anthropic.BetaTool{}
	if len(req.Tools) > 0 {
		var tools []any
		for _, t := range req.Tools {
			byName[t.Name()] = t
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name(), "description": t.Description(), "parameters": t.InputSchema(),
			}})
		}
		body["tools"] = tools
	}
	if req.Schema != nil {
		body["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{
			"name": "answer", "strict": true, "schema": schemaFor(req.Schema),
		}}
	}
	messages := []any{orMessage{Role: "system", Content: req.System}}
	for _, t := range req.History {
		messages = append(messages, orMessage{Role: t.Role, Content: t.Text})
	}
	messages = append(messages, orMessage{Role: "user", Content: req.Prompt})

	var prompt, completion int
	var cost float64
	defer func() {
		if prompt+completion > 0 {
			c.Logf("· %d input + %d output tokens, $%.4f", prompt, completion, cost)
		}
	}()
	for turn := 1; turn <= max(req.MaxTurns, 1); turn++ {
		c.Logf("· thinking (turn %d)", turn)
		body["messages"] = messages
		resp, err := c.postOpenRouter(ctx, base, key, body)
		if err != nil {
			return "", err
		}
		prompt += resp.Usage.PromptTokens
		completion += resp.Usage.CompletionTokens
		cost += resp.Usage.Cost
		if len(resp.Choices) == 0 {
			return "", errors.New("OpenRouter returned no choices")
		}
		choice := resp.Choices[0]
		var msg orAssistant
		if err := json.Unmarshal(choice.Message, &msg); err != nil {
			return "", fmt.Errorf("unreadable OpenRouter message: %w", err)
		}

		switch choice.FinishReason {
		case "length":
			return "", errors.New("response hit the output token limit before finishing")
		case "content_filter":
			return "", fmt.Errorf("request declined by the model's content filter %s", msg.Refusal)
		}
		if msg.Refusal != "" {
			return "", fmt.Errorf("request declined: %s", msg.Refusal)
		}
		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}

		messages = append(messages, choice.Message)
		for _, call := range msg.ToolCalls {
			messages = append(messages, orMessage{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    runTool(ctx, byName[call.Function.Name], call.Function.Name, call.Function.Arguments),
			})
		}
	}
	return "", fmt.Errorf("model was still researching after %d turns; try again or raise the limit", req.MaxTurns)
}

// runTool returns the tool's text output, or an error message the model can read.
func runTool(ctx context.Context, t anthropic.BetaTool, name, args string) string {
	if t == nil {
		return fmt.Sprintf("error: no tool named %q", name)
	}
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	blocks, err := t.Execute(ctx, json.RawMessage(args))
	if err != nil {
		return "error: " + err.Error()
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.OfText != nil {
			sb.WriteString(b.OfText.Text)
		}
	}
	return sb.String()
}

// postOpenRouter sends one completion request, retrying briefly on rate
// limits and transient server errors.
func (c *Client) postOpenRouter(ctx context.Context, base, key string, body any) (*orResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+key)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("X-Title", "learnx")
		res, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			return nil, err
		}

		var out orResponse
		_ = json.Unmarshal(raw, &out)
		retryable := res.StatusCode == 429 || res.StatusCode >= 500
		if retryable && attempt < 2 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
			}
			continue
		}
		if res.StatusCode != http.StatusOK || out.Error != nil {
			return nil, explainOpenRouter(res.StatusCode, out.Error, raw)
		}
		return &out, nil
	}
}

func explainOpenRouter(status int, e *orError, raw []byte) error {
	msg := strings.TrimSpace(string(raw))
	if e != nil && e.Message != "" {
		msg = e.Message
	}
	switch status {
	case 401, 403:
		return fmt.Errorf("OpenRouter rejected the key; check OPENROUTER_API_KEY: %s", msg)
	case 402:
		return fmt.Errorf("OpenRouter account is out of credits: %s", msg)
	case 429:
		return fmt.Errorf("rate limited by OpenRouter; wait a moment and retry: %s", msg)
	}
	return fmt.Errorf("OpenRouter error (HTTP %d): %s", status, msg)
}

// orEffort maps Claude effort levels onto OpenRouter's reasoning.effort.
func orEffort(e anthropic.BetaOutputConfigEffort) string {
	if e == anthropic.BetaOutputConfigEffortMax {
		return "xhigh"
	}
	return string(e)
}

// schemaFor builds a strict-mode JSON schema from a pointer to a struct:
// every field required, no additional properties, no $refs.
func schemaFor(v any) *jsonschema.Schema {
	r := jsonschema.Reflector{ExpandedStruct: true, DoNotReference: true}
	s := r.Reflect(v)
	s.Version, s.ID = "", ""
	return s
}
