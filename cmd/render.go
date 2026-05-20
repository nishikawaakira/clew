package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"aws-config-graph/internal/render"
	"aws-config-graph/internal/store"
)

func newRenderCmd() *cobra.Command {
	var (
		db     string
		view   string
		format string
		output string
		withEdgeLabels bool
	)
	cmd := &cobra.Command{
		Use:   "render",
		Short: "Render the imported graph as Mermaid.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if db == "" {
				return fmt.Errorf("--db is required")
			}
			if format != "mermaid" {
				return fmt.Errorf("only --format mermaid is supported (got %q)", format)
			}
			if view != "vpc" {
				return fmt.Errorf("only --view vpc is supported (got %q)", view)
			}
			return runRender(cmd, db, output, withEdgeLabels)
		},
	}
	cmd.Flags().StringVar(&db, "db", "", "Path to DuckDB database file")
	cmd.Flags().StringVar(&view, "view", "vpc", "Graph view to render (only 'vpc' is supported)")
	cmd.Flags().StringVar(&format, "format", "mermaid", "Output format (only 'mermaid' is supported)")
	cmd.Flags().StringVar(&output, "output", "", "Output file path (defaults to stdout)")
	cmd.Flags().BoolVar(&withEdgeLabels, "with-edge-labels", false, "Annotate edges with their relationship name")
	return cmd
}

func runRender(cmd *cobra.Command, dbPath, outputPath string, withEdgeLabels bool) error {
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	ctx := cmd.Context()
	nodes, err := st.ListNodes(ctx, render.VpcViewTypes)
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	edges, err := st.ListEdgesBetween(ctx, render.VpcViewTypes)
	if err != nil {
		return fmt.Errorf("list edges: %w", err)
	}

	var w io.Writer
	if outputPath == "" {
		w = cmd.OutOrStdout()
	} else {
		f, err := os.Create(outputPath)
		if err != nil {
			return fmt.Errorf("create %s: %w", outputPath, err)
		}
		defer f.Close()
		w = f
	}

	return render.Mermaid(w, nodes, edges, render.Options{WithEdgeLabels: withEdgeLabels})
}
