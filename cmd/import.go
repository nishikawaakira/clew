package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"aws-config-graph/internal/graph"
	"aws-config-graph/internal/model"
	"aws-config-graph/internal/snapshot"
	"aws-config-graph/internal/store"
)

func newImportCmd() *cobra.Command {
	var (
		input string
		db    string
	)
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import an AWS Config Configuration Snapshot into DuckDB.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if input == "" {
				return fmt.Errorf("--input is required")
			}
			if db == "" {
				return fmt.Errorf("--db is required")
			}
			return runImport(cmd, input, db)
		},
	}
	cmd.Flags().StringVar(&input, "input", "", "Path to snapshot .json or .json.gz file")
	cmd.Flags().StringVar(&db, "db", "", "Path to DuckDB database file")
	return cmd
}

func runImport(cmd *cobra.Command, inputPath, dbPath string) error {
	rc, err := snapshot.Open(inputPath)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer rc.Close()

	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	ctx := cmd.Context()
	w, err := st.BeginWriter(ctx)
	if err != nil {
		return fmt.Errorf("begin writer: %w", err)
	}

	var items, nodes, edges int
	err = snapshot.Stream(rc, func(item model.ConfigItem) error {
		items++
		if err := w.WriteConfigItem(item); err != nil {
			return err
		}
		built := graph.Build(item)
		if err := w.WriteNode(built.Node); err != nil {
			return err
		}
		nodes++
		for _, p := range built.PlaceholderNodes {
			if err := w.WriteNode(p); err != nil {
				return err
			}
		}
		for _, e := range built.Edges {
			if err := w.WriteEdge(e); err != nil {
				return err
			}
			edges++
		}
		return nil
	})
	if err != nil {
		w.Rollback()
		return fmt.Errorf("import: %w", err)
	}
	if err := w.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "imported %d items, %d real nodes, %d edges\n", items, nodes, edges)
	return nil
}
