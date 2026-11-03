package cli

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mlliarm/learnx/internal/llm"
	"github.com/mlliarm/learnx/internal/tutor"
)

func newChatCmd() *cobra.Command {
	var model string
	var raw bool
	cmd := &cobra.Command{
		Use:   "chat <pack> [question...]",
		Short: "Ask questions answered from the docs, with citations",
		Long: `Ask about the technology and get answers researched in its official docs,
with links to the pages used. With a question, answers it and exits; without,
starts a conversation (an empty line or Ctrl-D ends it).`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withTutor(cmd, args[0], model, "", func(ctx context.Context, t *tutor.Tutor) error {
				out := cmd.OutOrStdout()
				if len(args) > 1 {
					answer, err := t.Ask(ctx, nil, strings.Join(args[1:], " "))
					if err != nil {
						return err
					}
					return render(out, answer+"\n", raw)
				}

				fmt.Fprintf(out, "Ask about %s; an empty line ends the chat.\n\n", t.Pack.Title)
				in := bufio.NewScanner(cmd.InOrStdin())
				var history []llm.Turn
				for {
					fmt.Fprint(out, "you> ")
					if !in.Scan() {
						fmt.Fprintln(out)
						return in.Err()
					}
					q := strings.TrimSpace(in.Text())
					if q == "" {
						return nil
					}
					answer, err := t.Ask(ctx, history, q)
					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n\n", err)
						continue
					}
					if err := render(out, answer+"\n", raw); err != nil {
						return err
					}
					fmt.Fprintln(out)
					history = append(history, llm.Turn{Role: "user", Text: q}, llm.Turn{Role: "assistant", Text: answer})
				}
			})
		},
	}
	cmd.Flags().BoolVar(&raw, "raw", false, "print plain Markdown")
	addModelFlag(cmd, &model)
	return cmd
}
