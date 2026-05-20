package cmd

import (
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"aws-config-graph/internal/model"
	"aws-config-graph/internal/query"
	"aws-config-graph/internal/render"
	"aws-config-graph/internal/store"
)

func newQueryCmd() *cobra.Command {
	var (
		db         string
		resourceID string
		depth      int
		output     string
		format     string
		withEdgeLabels bool
	)
	cmd := &cobra.Command{
		Use:   "query",
		Short: "Inspect the neighborhood around a specific resource id.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if db == "" {
				return fmt.Errorf("--db is required")
			}
			if resourceID == "" {
				return fmt.Errorf("--resource-id is required")
			}
			if depth < 0 {
				return fmt.Errorf("--depth must be >= 0")
			}
			return runQuery(cmd, db, resourceID, depth, format, output, withEdgeLabels)
		},
	}
	cmd.Flags().StringVar(&db, "db", "", "Path to DuckDB database file")
	cmd.Flags().StringVar(&resourceID, "resource-id", "", "Resource id to start traversal from (e.g. vpc-xxxx)")
	cmd.Flags().IntVar(&depth, "depth", 1, "Traversal depth (0 = seed only)")
	cmd.Flags().StringVar(&output, "output", "", "Output file path (defaults to stdout)")
	cmd.Flags().StringVar(&format, "format", "mermaid", "Output format: 'mermaid' or 'text'")
	cmd.Flags().BoolVar(&withEdgeLabels, "with-edge-labels", true, "Annotate edges with their relationship name (mermaid only)")
	return cmd
}

func runQuery(cmd *cobra.Command, dbPath, resourceID string, depth int, format, outputPath string, withEdgeLabels bool) error {
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	ctx := cmd.Context()
	nodes, edges, err := query.BFS(ctx, st, resourceID, depth)
	if err != nil {
		return fmt.Errorf("bfs: %w", err)
	}
	if len(nodes) == 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "no nodes found for resource id %q\n", resourceID)
		return nil
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

	switch format {
	case "mermaid":
		return render.Mermaid(w, nodes, edges, render.Options{WithEdgeLabels: withEdgeLabels})
	case "text":
		return renderText(w, nodes, edges)
	default:
		return fmt.Errorf("unsupported --format %q (use 'mermaid' or 'text')", format)
	}
}

func renderText(w io.Writer, nodes []model.Node, edges []model.Edge) error {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].ResourceType != nodes[j].ResourceType {
			return nodes[i].ResourceType < nodes[j].ResourceType
		}
		return nodes[i].ResourceID < nodes[j].ResourceID
	})
	if _, err := fmt.Fprintln(w, "Nodes:"); err != nil {
		return err
	}
	for _, n := range nodes {
		marker := ""
		if isPlaceholderNode(n) {
			marker = " (placeholder)"
		}
		if _, err := fmt.Fprintf(w, "  - %s/%s%s\n", n.ResourceType, n.ResourceID, marker); err != nil {
			return err
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].SourceNodeID != edges[j].SourceNodeID {
			return edges[i].SourceNodeID < edges[j].SourceNodeID
		}
		return edges[i].TargetNodeID < edges[j].TargetNodeID
	})
	if _, err := fmt.Fprintln(w, "Edges:"); err != nil {
		return err
	}
	for _, e := range edges {
		if _, err := fmt.Fprintf(w, "  - %s ==[%s]==> %s\n", e.SourceNodeID, e.RelationshipName, e.TargetNodeID); err != nil {
			return err
		}
	}
	return nil
}

func isPlaceholderNode(n model.Node) bool {
	if n.Properties == nil {
		return false
	}
	if v, ok := n.Properties["placeholder"]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}
