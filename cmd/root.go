package cmd

import "github.com/spf13/cobra"

// NewRoot returns the root cobra command for the clew CLI.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "clew",
		Short:         "Find your clew through the AWS Config labyrinth.",
		Long: `clew imports AWS Config Configuration Snapshots into a local DuckDB,
then renders the resulting resource / relationship graph as an interactive
HTML page (Cytoscape.js + dagre), Graphviz image (SVG / PNG / DOT) or Mermaid.`,
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newImportCmd())
	root.AddCommand(newRenderCmd())
	root.AddCommand(newQueryCmd())
	return root
}
