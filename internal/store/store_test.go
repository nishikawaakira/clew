package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/marcboeker/go-duckdb"

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

// countConfigItems is a small helper used by the idempotency tests below.
func countConfigItems(t *testing.T, st *store.Store, where string, args ...any) int {
	t.Helper()
	q := "SELECT COUNT(*) FROM config_items"
	if where != "" {
		q += " WHERE " + where
	}
	row := st.DB().QueryRowContext(context.Background(), q, args...)
	var n int
	if err := row.Scan(&n); err != nil {
		t.Fatalf("count config_items: %v", err)
	}
	return n
}

// TestConfigItemsIdempotentOnReImport covers Finding #2: importing the same
// snapshot twice must not duplicate config_items.
func TestConfigItemsIdempotentOnReImport(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "testdata", "sample_snapshot.json")
	dbPath := filepath.Join(t.TempDir(), "test.duckdb")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	importOnce := func() {
		t.Helper()
		f, err := os.Open(fixturePath)
		if err != nil {
			t.Fatalf("open fixture: %v", err)
		}
		defer f.Close()
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			t.Fatalf("seek: %v", err)
		}
		w, err := st.BeginWriter(context.Background())
		if err != nil {
			t.Fatalf("begin writer: %v", err)
		}
		err = snapshot.Stream(f, func(item model.ConfigItem) error {
			return w.WriteConfigItem(item)
		})
		if err != nil {
			w.Rollback()
			t.Fatalf("stream: %v", err)
		}
		if err := w.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}

	importOnce()
	first := countConfigItems(t, st, "")
	if first == 0 {
		t.Fatalf("first import produced 0 rows, fixture broken?")
	}

	importOnce()
	second := countConfigItems(t, st, "")
	if second != first {
		t.Fatalf("re-importing the same snapshot is not idempotent: first=%d second=%d (delta=%d)",
			first, second, second-first)
	}
}

// TestConfigItemsDistinctOnDifferentCaptureTime covers Finding #3: snapshots
// taken at different capture times for the same logical resource must coexist
// as separate config_items rows so we don't silently drop history.
func TestConfigItemsDistinctOnDifferentCaptureTime(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.duckdb")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	base := model.ConfigItem{
		AccountID:    "123456789012",
		AwsRegion:    "ap-northeast-1",
		ResourceType: "AWS::EC2::VPC",
		ResourceID:   "vpc-history",
	}
	earlier := base
	earlier.ConfigurationItemCaptureTime = time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	later := base
	later.ConfigurationItemCaptureTime = time.Date(2026, 5, 21, 0, 0, 0, 0, time.UTC)

	w, err := st.BeginWriter(context.Background())
	if err != nil {
		t.Fatalf("begin writer: %v", err)
	}
	if err := w.WriteConfigItem(earlier); err != nil {
		t.Fatalf("write earlier: %v", err)
	}
	if err := w.WriteConfigItem(later); err != nil {
		t.Fatalf("write later: %v", err)
	}
	// Writing the *same* timestamp again must be a no-op (idempotency).
	if err := w.WriteConfigItem(later); err != nil {
		t.Fatalf("write later (dup): %v", err)
	}
	if err := w.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got := countConfigItems(t, st, "resource_id = ?", "vpc-history")
	if got != 2 {
		t.Fatalf("want 2 rows for vpc-history (two distinct capture_times, second dup ignored), got %d", got)
	}

	// ConfigItemID should reflect the capture_time so two snapshots produce
	// distinct ids — sanity-check directly.
	if store.ConfigItemID(earlier) == store.ConfigItemID(later) {
		t.Fatalf("ConfigItemID collapsed two different capture_times to the same id")
	}
}

// legacyConfigItemsSchema is the pre-PK table definition shipped by older
// versions of clew (and its predecessor `aws-config-graph`). Used to recreate
// the exact "old DB" state in migration tests.
const legacyConfigItemsSchema = `CREATE TABLE config_items (
    item_id TEXT,
    account_id TEXT,
    aws_region TEXT,
    resource_type TEXT,
    resource_id TEXT,
    resource_name TEXT,
    arn TEXT,
    capture_time TIMESTAMP,
    configuration_json TEXT,
    relationships_json TEXT,
    tags_json TEXT,
    raw_json TEXT
)`

// seedLegacyDB creates a DuckDB file with the *old* config_items schema
// (no PK) and inserts the given rows using the old item_id format
// (account:region:type:resource_id, without capture_time). Returns the
// path so tests can then reopen it via store.Open().
func seedLegacyDB(t *testing.T, rows []model.ConfigItem) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "legacy.duckdb")

	db, err := sql.Open("duckdb", dbPath)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(legacyConfigItemsSchema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	insert := `INSERT INTO config_items VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`
	for _, r := range rows {
		legacyID := r.AccountID + ":" + r.AwsRegion + ":" + r.ResourceType + ":" + r.ResourceID
		if _, err := db.Exec(insert,
			legacyID,
			r.AccountID, r.AwsRegion, r.ResourceType, r.ResourceID,
			r.ResourceName, r.ARN,
			r.ConfigurationItemCaptureTime,
			string(r.Configuration), nil, nil, string(r.Raw),
		); err != nil {
			t.Fatalf("insert legacy row %s: %v", legacyID, err)
		}
	}
	return dbPath
}

// TestMigrateLegacyConfigItemsSchema covers the auto-migration path: open an
// old-schema DB, verify Open() upgrades it in place, then verify a subsequent
// import works (the bug originally surfaced as a "Binder Error" at Prepare
// time on insertConfigItemSQL).
func TestMigrateLegacyConfigItemsSchema(t *testing.T) {
	captureTime := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	seedRows := []model.ConfigItem{
		{
			AccountID: "123456789012", AwsRegion: "ap-northeast-1",
			ResourceType: "AWS::EC2::VPC", ResourceID: "vpc-legacy",
			ResourceName:                 "legacy-vpc",
			ARN:                          "arn:aws:ec2:ap-northeast-1:123456789012:vpc/vpc-legacy",
			ConfigurationItemCaptureTime: captureTime,
			Configuration:                []byte(`{"cidr":"10.0.0.0/16"}`),
			Raw:                          []byte(`{"resourceId":"vpc-legacy"}`),
		},
	}
	dbPath := seedLegacyDB(t, seedRows)

	// Open should auto-migrate. Before the fix this still succeeded; it was
	// BeginWriter -> Prepare(insertConfigItemSQL) that failed. So the real
	// regression test is the WriteConfigItem below.
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open legacy db (migration should be automatic): %v", err)
	}
	defer st.Close()

	// New schema must now have the PK constraint.
	var pkCount int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM duckdb_constraints()
        WHERE table_name = 'config_items' AND constraint_type = 'PRIMARY KEY'`).Scan(&pkCount); err != nil {
		t.Fatalf("inspect post-migration constraints: %v", err)
	}
	if pkCount == 0 {
		t.Fatalf("PRIMARY KEY missing on config_items after migration")
	}

	// The legacy row must be preserved with the *new* item_id format.
	migratedID := store.ConfigItemID(seedRows[0])
	var seen string
	if err := st.DB().QueryRow(
		`SELECT item_id FROM config_items WHERE resource_id = ?`, "vpc-legacy",
	).Scan(&seen); err != nil {
		t.Fatalf("re-read migrated row: %v", err)
	}
	if seen != migratedID {
		t.Fatalf("migrated item_id mismatch:\n got:  %q\n want: %q", seen, migratedID)
	}

	// The legacy backup table must have been dropped.
	var backupCount int
	if err := st.DB().QueryRow(
		`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'config_items_old'`,
	).Scan(&backupCount); err != nil {
		t.Fatalf("check backup table: %v", err)
	}
	if backupCount != 0 {
		t.Fatalf("config_items_old should have been dropped, still present")
	}

	// Critical regression test: BeginWriter().WriteConfigItem() now works
	// (this is the path that used to fail with the DuckDB binder error).
	w, err := st.BeginWriter(context.Background())
	if err != nil {
		t.Fatalf("begin writer on migrated db: %v", err)
	}
	// Re-importing the *same* row must be a no-op (idempotency preserved
	// across the migration boundary).
	if err := w.WriteConfigItem(seedRows[0]); err != nil {
		t.Fatalf("write same-resource same-capture_time item: %v", err)
	}
	// A different capture_time must produce an additional row.
	laterRow := seedRows[0]
	laterRow.ConfigurationItemCaptureTime = captureTime.Add(24 * time.Hour)
	if err := w.WriteConfigItem(laterRow); err != nil {
		t.Fatalf("write later-capture_time item: %v", err)
	}
	if err := w.Commit(); err != nil {
		t.Fatalf("commit on migrated db: %v", err)
	}

	if got := countConfigItems(t, st, "resource_id = ?", "vpc-legacy"); got != 2 {
		t.Fatalf("want 2 rows post-import (1 migrated + 1 new capture_time, dup ignored), got %d", got)
	}
}

// TestMigrationIsNoOpOnNewSchema makes sure that opening an already-migrated
// (current-schema) DB doesn't accidentally re-migrate or drop data.
func TestMigrationIsNoOpOnNewSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fresh.duckdb")

	item := model.ConfigItem{
		AccountID:                    "123",
		AwsRegion:                    "ap-northeast-1",
		ResourceType:                 "AWS::EC2::VPC",
		ResourceID:                   "vpc-fresh",
		ConfigurationItemCaptureTime: time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC),
	}

	// First open: creates fresh new-schema DB and writes one row.
	st1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	w, err := st1.BeginWriter(context.Background())
	if err != nil {
		t.Fatalf("begin writer: %v", err)
	}
	if err := w.WriteConfigItem(item); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	st1.Close()

	// Second open: must not run migration, must not lose data.
	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen new-schema db: %v", err)
	}
	defer st2.Close()
	if got := countConfigItems(t, st2, "resource_id = ?", "vpc-fresh"); got != 1 {
		t.Fatalf("want 1 row preserved across reopen, got %d", got)
	}
	// And the migration backup table must not have appeared.
	var backupCount int
	if err := st2.DB().QueryRow(
		`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'config_items_old'`,
	).Scan(&backupCount); err != nil {
		t.Fatalf("check backup table: %v", err)
	}
	if backupCount != 0 {
		t.Fatalf("reopen of new-schema db should not create config_items_old")
	}
}

// TestMigrationStreamsLegacyRowsInBatches verifies that the migration walks
// the legacy table in bounded batches rather than buffering it whole. By
// setting MigrationBatchSize to a small value and seeding N >> batchSize rows,
// we exercise the rowid-paginated loop (multiple read/insert cycles within
// the same transaction). A regression to the old "buffer everything" form
// would still pass this test functionally — what guards us against that is
// the structural change (exported MigrationBatchSize + paginated SELECT) plus
// the fact that this test runs the loop ⌈N / batchSize⌉ times.
func TestMigrationStreamsLegacyRowsInBatches(t *testing.T) {
	const n = 30
	captureTime := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	items := make([]model.ConfigItem, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, model.ConfigItem{
			AccountID:                    "123456789012",
			AwsRegion:                    "ap-northeast-1",
			ResourceType:                 "AWS::EC2::Instance",
			ResourceID:                   fmt.Sprintf("i-%05d", i),
			ConfigurationItemCaptureTime: captureTime,
			Configuration:                []byte(`{"placeholder":"cfg"}`),
			Raw:                          []byte(`{"placeholder":"raw"}`),
		})
	}
	dbPath := seedLegacyDB(t, items)

	// Force the migration loop to iterate multiple times even though the
	// fixture is small. batchSize=7, n=30  →  5 read+insert cycles
	// (4 full + 1 short batch).
	prev := store.MigrationBatchSize
	store.MigrationBatchSize = 7
	defer func() { store.MigrationBatchSize = prev }()

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open + migrate legacy db: %v", err)
	}
	defer st.Close()

	if got := countConfigItems(t, st, ""); got != n {
		t.Fatalf("want %d rows migrated across batches, got %d", n, got)
	}

	// Every migrated row must carry the new item_id (which encodes
	// capture_time). Spot-check the first and last row of the fixture so a
	// pagination off-by-one (skipped first / dropped last batch) would fail.
	for _, idx := range []int{0, n - 1} {
		want := store.ConfigItemID(items[idx])
		var got string
		if err := st.DB().QueryRow(
			`SELECT item_id FROM config_items WHERE resource_id = ?`, items[idx].ResourceID,
		).Scan(&got); err != nil {
			t.Fatalf("re-read row %d: %v", idx, err)
		}
		if got != want {
			t.Fatalf("row %d item_id: got %q want %q", idx, got, want)
		}
	}

	// And the legacy backup must still be gone — proving that the per-batch
	// loop completed and reached the final DROP.
	var backupCount int
	if err := st.DB().QueryRow(
		`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'config_items_old'`,
	).Scan(&backupCount); err != nil {
		t.Fatalf("check backup table: %v", err)
	}
	if backupCount != 0 {
		t.Fatalf("config_items_old should be dropped after batched migration completed")
	}
}

// TestMigrationCollapsesLegacyDuplicates verifies that if the legacy (no-PK)
// table contained duplicate rows for the same resource+capture_time, the
// migration collapses them into a single row via the ON CONFLICT DO NOTHING
// in the new insert path.
func TestMigrationCollapsesLegacyDuplicates(t *testing.T) {
	captureTime := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	base := model.ConfigItem{
		AccountID:                    "123",
		AwsRegion:                    "ap-northeast-1",
		ResourceType:                 "AWS::EC2::VPC",
		ResourceID:                   "vpc-dup",
		ConfigurationItemCaptureTime: captureTime,
	}
	// Two identical legacy rows — only possible because old schema had no PK.
	dbPath := seedLegacyDB(t, []model.ConfigItem{base, base})

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open and migrate: %v", err)
	}
	defer st.Close()

	if got := countConfigItems(t, st, "resource_id = ?", "vpc-dup"); got != 1 {
		t.Fatalf("migration should collapse identical legacy duplicates; got %d rows", got)
	}
}
