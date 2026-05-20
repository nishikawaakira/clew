package cmd

import "github.com/spf13/cobra"

// NewRoot returns the root cobra command for the aws-config-graph CLI.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "aws-config-graph",
		Short:         "Import AWS Config snapshots into DuckDB and render graphs.",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newImportCmd())
	root.AddCommand(newRenderCmd())
	root.AddCommand(newQueryCmd())
	return root
}
