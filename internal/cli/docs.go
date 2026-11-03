package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mlliarm/learnx/internal/docs"
	"github.com/mlliarm/learnx/internal/pack"
)

func newDocsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docs",
		Short: "Query a pack's documentation source directly",
	}
	cmd.AddCommand(newDocsToolsCmd(), newDocsSearchCmd(), newDocsAskCmd())
	return cmd
}

func newDocsToolsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tools <pack>",
		Short: "List the tools exposed by the pack's docs MCP server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSource(cmd, args[0], func(ctx context.Context, src docs.Source) error {
				m, ok := src.(*docs.MCPSource)
				if !ok {
					return errors.New("docs source is not an MCP server")
				}
				tools, err := m.Tools(ctx)
				if err != nil {
					return err
				}
				for _, t := range tools {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\n  %s\n\n", t.Name, t.Description)
				}
				return nil
			})
		},
	}
}

func newDocsSearchCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "search <pack> <query...>",
		Short: "Search the documentation",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := strings.Join(args[1:], " ")
			return withSource(cmd, args[0], func(ctx context.Context, src docs.Source) error {
				results, err := src.Search(ctx, query)
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				for i, r := range results {
					if i == limit {
						break
					}
					fmt.Fprintf(out, "%d. %s\n", i+1, r.Title)
					if r.URL != "" {
						fmt.Fprintf(out, "   %s\n", r.URL)
					}
					fmt.Fprintf(out, "   %s\n\n", snippet(r.Text, 300))
				}
				return nil
			})
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 5, "maximum number of results")
	return cmd
}

func newDocsAskCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ask <pack> <question...>",
		Short: "Ask the docs source's QA agent a question",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			question := strings.Join(args[1:], " ")
			return withSource(cmd, args[0], func(ctx context.Context, src docs.Source) error {
				answer, err := src.Ask(ctx, question)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), answer)
				return nil
			})
		},
	}
}

func withSource(cmd *cobra.Command, packName string, fn func(context.Context, docs.Source) error) error {
	p, err := pack.Load(packName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
	defer cancel()
	src, err := docs.Open(ctx, p)
	if err != nil {
		return err
	}
	defer src.Close()
	return fn(ctx, src)
}

func snippet(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
