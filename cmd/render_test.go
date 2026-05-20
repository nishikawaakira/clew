package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"clew/internal/graph"
	"clew/internal/model"
	"clew/internal/snapshot"
	"clew/internal/store"
)

// populateTestDB writes the bundled snapshot fixture into a fresh DuckDB
// file inside t.TempDir() and returns the path. Used by integration-style
// tests of the render / query subcommands.
func populateTestDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.duckdb")

	fixturePath := filepath.Join("..", "testdata", "sample_snapshot.json")
	f, err := os.Open(fixturePath)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	w, err := st.BeginWriter(context.Background())
	if err != nil {
		t.Fatalf("begin writer: %v", err)
	}
	err = snapshot.Stream(f, func(item model.ConfigItem) error {
		if err := w.WriteConfigItem(item); err != nil {
			return err
		}
		built := graph.Build(item)
		if err := w.WriteNode(built.Node); err != nil {
			return err
		}
		for _, p := range built.PlaceholderNodes {
			if err := w.WriteNode(p); err != nil {
				return err
			}
		}
		for _, e := range built.Edges {
			if err := w.WriteEdge(e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		w.Rollback()
		t.Fatalf("stream: %v", err)
	}
	if err := w.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return dbPath
}

// TestRenderInvalidFormatPreservesExistingFile covers Finding #1:
// `render --format bogus --output existing_file` must fail without
// truncating the existing output file.
func TestRenderInvalidFormatPreservesExistingFile(t *testing.T) {
	dbPath := populateTestDB(t)
	outputPath := filepath.Join(filepath.Dir(dbPath), "existing.svg")
	original := []byte("DO NOT TRUNCATE")
	if err := os.WriteFile(outputPath, original, 0o644); err != nil {
		t.Fatalf("seed output file: %v", err)
	}

	root := NewRoot()
	root.SetArgs([]string{
		"render",
		"--db", dbPath,
		"--view", "vpc",
		"--format", "bogus",
		"--output", outputPath,
	})
	// Don't let cobra spam test logs.
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)

	if err := root.Execute(); err == nil {
		t.Fatal("expected error for --format bogus, got nil")
	}

	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("output file gone: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("output file was modified despite invalid format:\n got:  %q\n want: %q", got, original)
	}
}

// TestQueryInvalidFormatPreservesExistingFile is the query-side mirror of the
// render test — same Finding, same fix path (validateOutputPreconditions).
func TestQueryInvalidFormatPreservesExistingFile(t *testing.T) {
	dbPath := populateTestDB(t)
	outputPath := filepath.Join(filepath.Dir(dbPath), "existing.svg")
	original := []byte("DO NOT TRUNCATE")
	if err := os.WriteFile(outputPath, original, 0o644); err != nil {
		t.Fatalf("seed output file: %v", err)
	}

	root := NewRoot()
	root.SetArgs([]string{
		"query",
		"--db", dbPath,
		"--resource-id", "vpc-aaa",
		"--depth", "1",
		"--format", "bogus",
		"--output", outputPath,
	})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)

	if err := root.Execute(); err == nil {
		t.Fatal("expected error for --format bogus, got nil")
	}

	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("output file gone: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("output file was modified despite invalid format:\n got:  %q\n want: %q", got, original)
	}
}

// TestValidateOutputPreconditions exercises the boundary cases of the helper
// directly so future tweaks don't regress them.
func TestValidateOutputPreconditions(t *testing.T) {
	tests := []struct {
		name       string
		format     string
		outputPath string
		allowText  bool
		wantErr    bool
	}{
		{"html ok stdout", "html", "", false, false},
		{"svg ok stdout", "svg", "", false, false},
		{"png needs output", "png", "", false, true},
		{"jpg needs output", "jpg", "", false, true},
		{"png with output ok", "png", "/tmp/x.png", false, false},
		{"text rejected by render", "text", "", false, true},
		{"text accepted by query", "text", "", true, false},
		{"bogus format rejected", "bogus", "/tmp/x.svg", false, true},
		{"bogus format rejected (query)", "bogus", "/tmp/x.svg", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOutputPreconditions(tc.format, tc.outputPath, tc.allowText)
			if (err != nil) != tc.wantErr {
				t.Fatalf("want err=%v, got %v", tc.wantErr, err)
			}
		})
	}
}
