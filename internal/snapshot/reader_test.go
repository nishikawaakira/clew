package snapshot

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestStreamReadsConfigurationItems(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "sample_snapshot.json")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	items, err := ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(items) != 6 {
		t.Fatalf("want 6 items, got %d", len(items))
	}
	if items[0].ResourceType != "AWS::EC2::VPC" {
		t.Fatalf("want first item VPC, got %s", items[0].ResourceType)
	}
	if items[0].ResourceID != "vpc-aaa" {
		t.Fatalf("want resourceId vpc-aaa, got %s", items[0].ResourceID)
	}
	if len(items[0].Raw) == 0 {
		t.Fatalf("expected Raw to be populated")
	}
}

func TestOpenAutoDetectsGzip(t *testing.T) {
	// Build a gzipped copy of the fixture in a temp file.
	path := filepath.Join("..", "..", "testdata", "sample_snapshot.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	tmp := filepath.Join(t.TempDir(), "snapshot.json.gz")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}

	rc, err := Open(tmp)
	if err != nil {
		t.Fatalf("open gz: %v", err)
	}
	defer rc.Close()
	items, err := ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll(gz): %v", err)
	}
	if len(items) != 6 {
		t.Fatalf("want 6 items, got %d", len(items))
	}
}
