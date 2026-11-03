package tutor

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/mlliarm/learnx/internal/llm"
)

// Ask answers a question from the docs, citing the pages it used. history
// holds the earlier turns of the conversation.
func (t *Tutor) Ask(ctx context.Context, history []llm.Turn, question string) (string, error) {
	system := fmt.Sprintf(`You answer a learner's questions about %[1]s using the official %[1]s documentation, which you reach through the docs tools.

Search the docs before answering, even when you think you know: the product changes between releases. Answer in Markdown, concisely, with a short example when it helps. Link the documentation pages that support each claim right where you make it, on the words the claim is about, as in [data streams](URL), never as a bare URL, and end with a "Sources" list of linked page titles. Only cite URLs that appeared in tool results. If the docs don't cover the question, say so plainly instead of guessing.%[2]s`,
		t.Pack.Title, t.baseURLNote())
	answer, err := t.LLM.Run(ctx, llm.Request{
		System:   system,
		History:  history,
		Prompt:   question,
		Tools:    t.Tools,
		Effort:   anthropic.BetaOutputConfigEffortMedium,
		MaxTurns: 12,
	})
	return strings.TrimSpace(answer), err
}
