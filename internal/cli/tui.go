package cli

import (
	"github.com/spf13/cobra"

	"github.com/mlliarm/learnx/internal/pack"
	"github.com/mlliarm/learnx/internal/tui"
)

func newTUICmd() *cobra.Command {
	var model, effort string
	cmd := &cobra.Command{
		Use:   "tui <pack>",
		Short: "Full-screen course: syllabus, lessons, practice and review",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := pack.Load(args[0])
			if err != nil {
				return err
			}
			st, syl, err := loadSyllabus(p.Name)
			if err != nil {
				return err
			}
			defer st.Close()
			return tui.Run(cmd.Context(), tui.Options{Pack: p, Store: st, Syllabus: syl, Model: model, Effort: effort})
		},
	}
	cmd.Flags().StringVar(&effort, "effort", "high", "model effort for lesson writing: low, medium, high, xhigh or max")
	addModelFlag(cmd, &model)
	return cmd
}
