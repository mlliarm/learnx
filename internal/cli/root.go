// Package cli wires up the learnx Cobra commands.
package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mlliarm/learnx/internal/pack"
)

func Execute() error {
	return newRootCmd().Execute()
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:          "learnx",
		Short:        "Interactive tutorials generated from a technology's public docs",
		SilenceUsage: true,
	}
	root.AddCommand(newPacksCmd(), newDocsCmd(), newPlanCmd(), newSyllabusCmd(), newLessonCmd(), newPracticeCmd(), newReviewCmd(), newTUICmd(), newChatCmd())
	return root
}

func newPacksCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "packs",
		Short: "List available tech packs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			for _, name := range pack.Names() {
				p, err := pack.Load(name)
				if err != nil {
					return err
				}
				fmt.Fprintf(w, "%s\t%s\n", p.Name, p.Description)
			}
			return w.Flush()
		},
	}
}
