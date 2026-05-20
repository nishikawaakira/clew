package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"clew/internal/graph"
	"clew/internal/model"
	"clew/internal/snapshot"
	"clew/internal/store"
)

// TestImportAndQuery exercises Open → BeginWriter → WriteConfigItem/WriteNode/WriteEdge
// → ListNodes/ListEdgesBetween → FindNodesByResourceID → NeighborEdges against a real
// DuckDB file backed by the sample snapshot fixture.
func TestImportAndQuery(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "testdata", "sample_snapshot.json")
	f, err := os.Open(fixturePath)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	dbPath := filepath.Join(t.TempDir(), "test.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	w, err := st.BeginWriter(ctx)
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

	// All real nodes from the fixture should now exist as real (non-placeholder) nodes.
	vpcNodes, err := st.FindNodesByResourceID(ctx, "vpc-aaa")
	if err != nil {
		t.Fatalf("find vpc: %v", err)
	}
	if len(vpcNodes) != 1 {
		t.Fatalf("want 1 vpc node, got %d", len(vpcNodes))
	}
	if v, ok := vpcNodes[0].Properties["placeholder"]; ok && v == true {
		t.Fatalf("vpc-aaa should be a real node, found placeholder=true")
	}

	// sg-002 is only referenced from sg-001 ingress rule, never imported directly,
	// so it must exist as a placeholder.
	sgPlaceholders, err := st.FindNodesByResourceID(ctx, "sg-002")
	if err != nil {
		t.Fatalf("find sg-002: %v", err)
	}
	if len(sgPlaceholders) != 1 {
		t.Fatalf("want 1 sg-002 placeholder, got %d", len(sgPlaceholders))
	}
	if v, ok := sgPlaceholders[0].Properties["placeholder"]; !ok || v != true {
		t.Fatalf("sg-002 should be placeholder=true, got %+v", sgPlaceholders[0].Properties)
	}

	// Edge from i-1111 -> subnet-pub should exist.
	enis, err := st.FindNodesByResourceID(ctx, "i-1111")
	if err != nil || len(enis) != 1 {
		t.Fatalf("find instance: err=%v len=%d", err, len(enis))
	}
	edges, err := st.NeighborEdges(ctx, []string{enis[0].NodeID})
	if err != nil {
		t.Fatalf("neighbor edges: %v", err)
	}
	var sawSubnet bool
	for _, e := range edges {
		if e.TargetType == "AWS::EC2::Subnet" {
			sawSubnet = true
		}
	}
	if !sawSubnet {
		t.Fatalf("expected an edge from instance to subnet; got %+v", edges)
	}
}
