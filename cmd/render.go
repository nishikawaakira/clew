package cmd

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"aws-config-graph/internal/model"
	"aws-config-graph/internal/render"
	"aws-config-graph/internal/store"
)

func newRenderCmd() *cobra.Command {
	var (
		db             string
		view           string
		format         string
		output         string
		layout         string
		withEdgeLabels bool
	)
	cmd := &cobra.Command{
		Use:   "render",
		Short: "Render the imported graph (Mermaid text or Graphviz image).",
		Long: `Render the imported graph into one of:
  mermaid  - Markdown-fenced Mermaid 'graph TD' text
  dot      - Graphviz DOT source
  svg      - Vector image (recommended for documentation)
  png      - Raster image
  jpg      - Raster image`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if db == "" {
				return fmt.Errorf("--db is required")
			}
			if view != "vpc" {
				return fmt.Errorf("only --view vpc is supported (got %q)", view)
			}
			return runRender(cmd, db, format, output, layout, withEdgeLabels)
		},
	}
	cmd.Flags().StringVar(&db, "db", "", "Path to DuckDB database file")
	cmd.Flags().StringVar(&view, "view", "vpc", "Graph view to render (only 'vpc' is supported)")
	cmd.Flags().StringVar(&format, "format", "mermaid", "Output format: mermaid|dot|svg|png|jpg")
	cmd.Flags().StringVar(&output, "output", "", "Output file path (defaults to stdout)")
	cmd.Flags().StringVar(&layout, "layout", "dot", "Graphviz layout engine (dot|neato|fdp|circo|twopi). Ignored for mermaid")
	cmd.Flags().BoolVar(&withEdgeLabels, "with-edge-labels", false, "Annotate edges with their relationship name")
	return cmd
}

func runRender(cmd *cobra.Command, dbPath, format, outputPath, layout string, withEdgeLabels bool) error {
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

	w, closer, err := openOutput(cmd, outputPath, format)
	if err != nil {
		return err
	}
	defer closer()

	return dispatchFormat(ctx, w, nodes, edges, format, layout, withEdgeLabels)
}

// openOutput returns an io.Writer for the chosen output path. If outputPath is
// empty, stdout is used — but binary formats refuse stdout to avoid garbling
// the terminal.
func openOutput(cmd *cobra.Command, outputPath, format string) (io.Writer, func(), error) {
	if outputPath == "" {
		if isBinaryFormat(format) {
			return nil, func() {}, fmt.Errorf("binary format %q requires --output <file>", format)
		}
		return cmd.OutOrStdout(), func() {}, nil
	}
	f, err := os.Create(outputPath)
	if err != nil {
		return nil, func() {}, fmt.Errorf("create %s: %w", outputPath, err)
	}
	return f, func() { f.Close() }, nil
}

func isBinaryFormat(format string) bool {
	switch format {
	case "png", "jpg":
		return true
	default:
		return false
	}
}

// dispatchFormat handles all format choices in one place so render and query
// stay in sync.
func dispatchFormat(ctx context.Context, w io.Writer, nodes []model.Node, edges []model.Edge, format, layout string, withEdgeLabels bool) error {
	switch format {
	case "mermaid":
		return render.Mermaid(w, nodes, edges, render.Options{WithEdgeLabels: withEdgeLabels})
	case "dot":
		return render.Graphviz(ctx, w, nodes, edges, render.GraphvizOptions{Format: render.GraphvizDOT, Layout: layout, WithEdgeLabels: withEdgeLabels})
	case "svg":
		return render.Graphviz(ctx, w, nodes, edges, render.GraphvizOptions{Format: render.GraphvizSVG, Layout: layout, WithEdgeLabels: withEdgeLabels})
	case "png":
		return render.Graphviz(ctx, w, nodes, edges, render.GraphvizOptions{Format: render.GraphvizPNG, Layout: layout, WithEdgeLabels: withEdgeLabels})
	case "jpg":
		return render.Graphviz(ctx, w, nodes, edges, render.GraphvizOptions{Format: render.GraphvizJPG, Layout: layout, WithEdgeLabels: withEdgeLabels})
	default:
		return fmt.Errorf("unsupported --format %q (use one of: mermaid, dot, svg, png, jpg)", format)
	}
}
