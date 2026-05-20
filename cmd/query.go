package cmd

import (
	"fmt"
	"io"
	"sort"

	"github.com/spf13/cobra"

	"clew/internal/model"
	"clew/internal/query"
	"clew/internal/store"
)

func newQueryCmd() *cobra.Command {
	var (
		db             string
		resourceID     string
		depth          int
		output         string
		format         string
		layout         string
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
			return runQuery(cmd, db, resourceID, depth, format, output, layout, withEdgeLabels)
		},
	}
	cmd.Flags().StringVar(&db, "db", "", "Path to DuckDB database file")
	cmd.Flags().StringVar(&resourceID, "resource-id", "", "Resource id to start traversal from (e.g. vpc-xxxx)")
	cmd.Flags().IntVar(&depth, "depth", 1, "Traversal depth (0 = seed only)")
	cmd.Flags().StringVar(&output, "output", "", "Output file path (defaults to stdout)")
	cmd.Flags().StringVar(&format, "format", "html", "Output format: html|mermaid|dot|svg|png|jpg|text")
	cmd.Flags().StringVar(&layout, "layout", "dot", "Graphviz layout engine (ignored for mermaid/text)")
	cmd.Flags().BoolVar(&withEdgeLabels, "with-edge-labels", true, "Annotate edges with their relationship name (mermaid/graphviz only)")
	return cmd
}

func runQuery(cmd *cobra.Command, dbPath, resourceID string, depth int, format, outputPath, layout string, withEdgeLabels bool) error {
	// Pre-validate so a bogus --format never truncates an existing --output file
	// via openOutput's os.Create.
	if err := validateOutputPreconditions(format, outputPath, true); err != nil {
		return err
	}

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

	w, closer, err := openOutput(cmd, outputPath, format)
	if err != nil {
		return err
	}
	defer closer()

	if format == "text" {
		return renderText(w, nodes, edges)
	}
	return dispatchFormat(ctx, w, nodes, edges, format, layout, withEdgeLabels)
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
