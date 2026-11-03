// Package llm runs tool-use loops over a pack's documentation tools, on
// Claude directly or on other models through OpenRouter.
package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const DefaultModel = "claude-opus-5-5"

type Client struct {
	api   anthropic.Client
	Model string
	// Status receives one-line progress notes (tool calls, turns); may be nil.
	Status io.Writer
}

// New returns a client for model. Model IDs of the form "vendor/model" run on
// OpenRouter (key from OPENROUTER_API_KEY); anything else is a Claude model.
func New(model string, status io.Writer, opts ...option.RequestOption) *Client {
	if model == "" {
		model = DefaultModel
	}
	// Credentials resolve from ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN or an `ant auth login` profile.
	return &Client{api: anthropic.NewClient(opts...), Model: model, Status: status}
}

// ModelFromEnv is LEARNX_MODEL if set, else OpenRouterModel when only an
// OpenRouter key is configured, else DefaultModel.
func ModelFromEnv() string {
	if m := os.Getenv("LEARNX_MODEL"); m != "" {
		return m
	}
	if os.Getenv("OPENROUTER_API_KEY") != "" && os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("ANTHROPIC_AUTH_TOKEN") == "" {
		return OpenRouterModel
	}
	return DefaultModel
}

// Turn is an earlier message in a conversation.
type Turn struct {
	Role string // "user" or "assistant"
	Text string
}

type Request struct {
	System string
	// History holds earlier turns of a conversation; Prompt follows them.
	History []Turn
	Prompt  string
	Tools   []anthropic.BetaTool
	Effort  anthropic.BetaOutputConfigEffort
	// Schema, when set, is a pointer to a struct describing the JSON the
	// final answer must match (structured outputs).
	Schema any
	// MaxTurns caps API round-trips so a research loop can't run away.
	MaxTurns int
}

// Run drives the tool loop to completion and returns the final text.
func (c *Client) Run(ctx context.Context, req Request) (string, error) {
	if isOpenRouter(c.Model) {
		return c.runOpenRouter(ctx, req)
	}
	params := anthropic.BetaToolRunnerParams{
		BetaMessageNewParams: anthropic.BetaMessageNewParams{
			Model:        c.Model,
			MaxTokens:    64000,
			System:       []anthropic.BetaTextBlockParam{{Text: req.System}},
			Messages:     anthropicMessages(req),
			OutputConfig: anthropic.BetaOutputConfigParam{Effort: req.Effort},
			CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
			// On a safety-classifier refusal, let the API re-serve the request
			// on a suitable fallback model instead of failing.
			Fallbacks: anthropic.BetaFallbacksParamOfDefault(),
			Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		},
		MaxIterations: req.MaxTurns,
	}
	if req.Schema != nil {
		params.OutputConfig.Format = anthropic.BetaJSONOutputFormatParam{Schema: req.Schema}
	}

	runner := c.api.Beta.Messages.NewToolRunnerStreaming(req.Tools, params)
	var last anthropic.BetaMessage
	turn := 0
	for events, err := range runner.AllStreaming(ctx) {
		if err != nil {
			return "", explain(err)
		}
		msg := anthropic.BetaMessage{}
		for ev, err := range events {
			if err != nil {
				return "", explain(err)
			}
			if msg.ID == "" {
				turn++
				c.Logf("· thinking (turn %d)", turn)
			}
			if err := msg.Accumulate(ev); err != nil {
				return "", err
			}
		}
		// The runner yields one empty sequence after the final turn.
		if msg.ID != "" {
			last = msg
		}
	}

	switch last.StopReason {
	case anthropic.BetaStopReasonEndTurn, anthropic.BetaStopReasonStopSequence:
	case anthropic.BetaStopReasonToolUse:
		return "", fmt.Errorf("model was still researching after %d turns; try again or raise the limit", turn)
	case anthropic.BetaStopReasonMaxTokens:
		return "", errors.New("response hit the output token limit before finishing")
	case anthropic.BetaStopReasonRefusal:
		return "", fmt.Errorf("request declined (%s): %s", last.StopDetails.Category, last.StopDetails.Explanation)
	default:
		return "", fmt.Errorf("unexpected stop reason %q", last.StopReason)
	}

	var sb strings.Builder
	for _, block := range last.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			sb.WriteString(t.Text)
		}
	}
	return sb.String(), nil
}

// Logf writes a progress line to Status, if set.
func (c *Client) Logf(format string, args ...any) {
	if c.Status != nil {
		fmt.Fprintf(c.Status, format+"\n", args...)
	}
}

func anthropicMessages(req Request) []anthropic.BetaMessageParam {
	var msgs []anthropic.BetaMessageParam
	for _, t := range req.History {
		role := anthropic.BetaMessageParamRoleUser
		if t.Role == "assistant" {
			role = anthropic.BetaMessageParamRoleAssistant
		}
		msgs = append(msgs, anthropic.BetaMessageParam{
			Role:    role,
			Content: []anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(t.Text)},
		})
	}
	return append(msgs, anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(req.Prompt)))
}

func explain(err error) error {
	var apierr *anthropic.Error
	if errors.As(err, &apierr) {
		switch apierr.StatusCode {
		case 401, 403:
			return fmt.Errorf("Claude API rejected the credentials; set ANTHROPIC_API_KEY or run `ant auth login`: %w", err)
		case 429:
			return fmt.Errorf("rate limited by the Claude API; wait a moment and retry: %w", err)
		}
	}
	return err
}
