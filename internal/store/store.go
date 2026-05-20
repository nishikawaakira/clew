package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb"

	"clew/internal/model"
)

// ConfigItemID returns the canonical PRIMARY KEY for config_items. It
// concatenates the logical resource identifier with the snapshot capture time
// so that:
//
//   - importing the same snapshot twice is a no-op (same item_id → ON CONFLICT
//     DO NOTHING in WriteConfigItem);
//   - snapshots captured at different points in time produce distinct rows
//     for the same resource, preserving history.
//
// Format: account_id:aws_region:resource_type:resource_id:captureTimeRFC3339Nano.
// The capture time is normalised to UTC so that two equivalent timestamps in
// different zones map to the same id.
func ConfigItemID(item model.ConfigItem) string {
	return fmt.Sprintf("%s:%s:%s:%s:%s",
		item.AccountID,
		item.AwsRegion,
		item.ResourceType,
		item.ResourceID,
		item.ConfigurationItemCaptureTime.UTC().Format(time.RFC3339Nano),
	)
}

// Store is a DuckDB-backed persistence layer for config_items, graph_nodes,
// and graph_edges.
type Store struct {
	db *sql.DB
}

// Open opens or creates a DuckDB database at path and ensures the schema exists.
func Open(path string) (*Store, error) {
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, fmt.Errorf("open duckdb: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping duckdb: %w", err)
	}
	s := &Store{db: db}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the raw handle for advanced callers / tests.
func (s *Store) DB() *sql.DB { return s.db }

// Schemas. config_items.item_id encodes account/region/type/resource-id AND
// the capture_time so that:
//   - re-importing the same snapshot is idempotent (the same item_id collides
//     and ON CONFLICT DO NOTHING drops the duplicate insert);
//   - importing snapshots taken at *different* capture times keeps a row per
//     (resource, capture_time) — i.e., history survives.
// See ConfigItemID for the exact format.
const (
	configItemsCreateSQL = `CREATE TABLE IF NOT EXISTS config_items (
        item_id TEXT PRIMARY KEY,
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

	graphNodesCreateSQL = `CREATE TABLE IF NOT EXISTS graph_nodes (
        node_id TEXT PRIMARY KEY,
        account_id TEXT,
        aws_region TEXT,
        resource_type TEXT,
        resource_id TEXT,
        resource_name TEXT,
        arn TEXT,
        label TEXT,
        properties_json TEXT
    )`

	graphEdgesCreateSQL = `CREATE TABLE IF NOT EXISTS graph_edges (
        edge_id TEXT PRIMARY KEY,
        source_node_id TEXT,
        target_node_id TEXT,
        relationship_name TEXT,
        source_resource_type TEXT,
        target_resource_type TEXT,
        properties_json TEXT
    )`
)

func (s *Store) init() error {
	ctx := context.Background()

	// Old DBs (created before item_id got a PRIMARY KEY) need to be migrated
	// in place — otherwise `INSERT ... ON CONFLICT (item_id) DO NOTHING`
	// fails at Prepare time with a cryptic DuckDB binder error. Must run
	// before the CREATE TABLE IF NOT EXISTS below, which would otherwise
	// silently skip the existing legacy table.
	if err := s.migrateConfigItemsIfNeeded(ctx); err != nil {
		return fmt.Errorf("migrate config_items: %w", err)
	}

	for _, sch := range []string{configItemsCreateSQL, graphNodesCreateSQL, graphEdgesCreateSQL} {
		if _, err := s.db.Exec(sch); err != nil {
			return fmt.Errorf("init schema: %w", err)
		}
	}
	return nil
}

// migrateConfigItemsIfNeeded upgrades a legacy config_items table (no PRIMARY
// KEY on item_id, item_id format without capture_time) to the current schema.
// New-schema and brand-new databases are a no-op.
func (s *Store) migrateConfigItemsIfNeeded(ctx context.Context) error {
	exists, err := s.configItemsExists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	hasPK, err := s.configItemsHasPrimaryKey(ctx)
	if err != nil {
		return err
	}
	if hasPK {
		return nil
	}
	return s.runConfigItemsMigration(ctx)
}

func (s *Store) configItemsExists(ctx context.Context) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'config_items'`,
	).Scan(&n); err != nil {
		return false, fmt.Errorf("check config_items existence: %w", err)
	}
	return n > 0, nil
}

func (s *Store) configItemsHasPrimaryKey(ctx context.Context) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM duckdb_constraints()
         WHERE table_name = 'config_items' AND constraint_type = 'PRIMARY KEY'`,
	).Scan(&n); err != nil {
		return false, fmt.Errorf("inspect config_items constraints: %w", err)
	}
	return n > 0, nil
}

// MigrationBatchSize controls how many legacy config_items rows are migrated
// per round-trip when an old-schema DB is upgraded by Open(). Migration walks
// the legacy table in rowid-ordered windows of this size, so peak memory is
// bounded to roughly batchSize * row_size instead of the whole table.
//
// Exposed as a var so tests can shrink it to exercise the multi-batch loop
// without seeding huge fixtures; production callers don't normally need to
// tune this.
var MigrationBatchSize = 256

// runConfigItemsMigration performs the actual in-place upgrade. Everything
// runs inside a single transaction (DuckDB supports transactional DDL), so a
// failure mid-migration rolls back to the pre-migration state.
//
// Memory: legacy rows are read in rowid-ordered batches of MigrationBatchSize,
// not all at once — config_items rows can contain large raw_json /
// configuration_json blobs, so loading the whole table at once is unsafe for
// large legacy DBs. New item_ids are still computed in Go via ConfigItemID()
// so they match exactly what subsequent imports will produce, keeping
// idempotency intact across the migration boundary.
//
// Concurrency: within a single *sql.Tx the underlying connection is shared,
// so we must close each read cursor before issuing INSERTs on the same tx.
// The batch loop respects that ordering explicitly.
func (s *Store) runConfigItemsMigration(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	// Clean up any artifact left behind by a previous failed attempt.
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS config_items_old`); err != nil {
		return fmt.Errorf("drop stale config_items_old: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE config_items RENAME TO config_items_old`); err != nil {
		return fmt.Errorf("rename legacy table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, configItemsCreateSQL); err != nil {
		return fmt.Errorf("create new config_items: %w", err)
	}

	ins, err := tx.PrepareContext(ctx, insertConfigItemSQL)
	if err != nil {
		return fmt.Errorf("prepare migrated insert: %w", err)
	}
	defer ins.Close()

	batchSize := MigrationBatchSize
	if batchSize <= 0 {
		batchSize = 256
	}

	type legacyRow struct {
		RowID                                          int64
		AccountID, AwsRegion, ResourceType, ResourceID string
		ResourceName, ARN                              sql.NullString
		CaptureTime                                    time.Time
		CfgJSON, RelsJSON, TagsJSON, RawJSON           sql.NullString
	}

	const selectBatchSQL = `
        SELECT rowid, account_id, aws_region, resource_type, resource_id,
               resource_name, arn, capture_time,
               configuration_json, relationships_json, tags_json, raw_json
        FROM config_items_old
        WHERE rowid > ?
        ORDER BY rowid
        LIMIT ?`

	batch := make([]legacyRow, 0, batchSize)
	var lastRowID int64 = -1
	for {
		// Read one window. Ordering by rowid + (rowid > last) gives an exact
		// once-through walk even if the table contains legacy duplicates.
		rows, err := tx.QueryContext(ctx, selectBatchSQL, lastRowID, batchSize)
		if err != nil {
			return fmt.Errorf("scan legacy rows: %w", err)
		}
		batch = batch[:0]
		for rows.Next() {
			var r legacyRow
			if err := rows.Scan(
				&r.RowID,
				&r.AccountID, &r.AwsRegion, &r.ResourceType, &r.ResourceID,
				&r.ResourceName, &r.ARN, &r.CaptureTime,
				&r.CfgJSON, &r.RelsJSON, &r.TagsJSON, &r.RawJSON,
			); err != nil {
				rows.Close()
				return fmt.Errorf("scan legacy row: %w", err)
			}
			batch = append(batch, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate legacy rows: %w", err)
		}
		// Cursor must be closed before we issue INSERTs on the same tx —
		// otherwise the driver would have two statements active on the same
		// underlying connection.
		rows.Close()

		if len(batch) == 0 {
			break
		}

		for _, r := range batch {
			itemID := ConfigItemID(model.ConfigItem{
				AccountID:                    r.AccountID,
				AwsRegion:                    r.AwsRegion,
				ResourceType:                 r.ResourceType,
				ResourceID:                   r.ResourceID,
				ConfigurationItemCaptureTime: r.CaptureTime,
			})
			// ON CONFLICT DO NOTHING in insertConfigItemSQL collapses any
			// duplicates the legacy (no-PK) table allowed.
			if _, err := ins.ExecContext(ctx,
				itemID,
				r.AccountID, r.AwsRegion, r.ResourceType, r.ResourceID,
				nullStringAsAny(r.ResourceName), nullStringAsAny(r.ARN),
				r.CaptureTime,
				nullStringAsAny(r.CfgJSON), nullStringAsAny(r.RelsJSON),
				nullStringAsAny(r.TagsJSON), nullStringAsAny(r.RawJSON),
			); err != nil {
				return fmt.Errorf("re-insert migrated row %s: %w", itemID, err)
			}
		}

		lastRowID = batch[len(batch)-1].RowID
		// A short batch means we've drained the table; skip the extra empty
		// round-trip.
		if len(batch) < batchSize {
			break
		}
	}

	if _, err := tx.ExecContext(ctx, `DROP TABLE config_items_old`); err != nil {
		return fmt.Errorf("drop legacy backup: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

func nullStringAsAny(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}

// Writer batches inserts inside a single transaction.
type Writer struct {
	tx                *sql.Tx
	insertConfigItem  *sql.Stmt
	upsertNode        *sql.Stmt
	insertPlaceholder *sql.Stmt
	insertEdge        *sql.Stmt
}

const (
	upsertNodeSQL = `INSERT INTO graph_nodes
        (node_id, account_id, aws_region, resource_type, resource_id, resource_name, arn, label, properties_json)
        VALUES (?,?,?,?,?,?,?,?,?)
        ON CONFLICT (node_id) DO UPDATE SET
            account_id = EXCLUDED.account_id,
            aws_region = EXCLUDED.aws_region,
            resource_type = EXCLUDED.resource_type,
            resource_id = EXCLUDED.resource_id,
            resource_name = EXCLUDED.resource_name,
            arn = EXCLUDED.arn,
            label = EXCLUDED.label,
            properties_json = EXCLUDED.properties_json`

	insertPlaceholderSQL = `INSERT INTO graph_nodes
        (node_id, account_id, aws_region, resource_type, resource_id, resource_name, arn, label, properties_json)
        VALUES (?,?,?,?,?,?,?,?,?)
        ON CONFLICT (node_id) DO NOTHING`

	insertEdgeSQL = `INSERT INTO graph_edges
        (edge_id, source_node_id, target_node_id, relationship_name, source_resource_type, target_resource_type, properties_json)
        VALUES (?,?,?,?,?,?,?)
        ON CONFLICT (edge_id) DO NOTHING`

	insertConfigItemSQL = `INSERT INTO config_items
        (item_id, account_id, aws_region, resource_type, resource_id, resource_name, arn, capture_time, configuration_json, relationships_json, tags_json, raw_json)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
        ON CONFLICT (item_id) DO NOTHING`
)

// BeginWriter starts a new transaction and prepares insert statements.
func (s *Store) BeginWriter(ctx context.Context) (*Writer, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	w := &Writer{tx: tx}
	var prepErr error
	w.insertConfigItem, prepErr = tx.PrepareContext(ctx, insertConfigItemSQL)
	if prepErr != nil {
		tx.Rollback()
		return nil, fmt.Errorf("prepare config_item: %w", prepErr)
	}
	w.upsertNode, prepErr = tx.PrepareContext(ctx, upsertNodeSQL)
	if prepErr != nil {
		tx.Rollback()
		return nil, fmt.Errorf("prepare upsert node: %w", prepErr)
	}
	w.insertPlaceholder, prepErr = tx.PrepareContext(ctx, insertPlaceholderSQL)
	if prepErr != nil {
		tx.Rollback()
		return nil, fmt.Errorf("prepare placeholder: %w", prepErr)
	}
	w.insertEdge, prepErr = tx.PrepareContext(ctx, insertEdgeSQL)
	if prepErr != nil {
		tx.Rollback()
		return nil, fmt.Errorf("prepare edge: %w", prepErr)
	}
	return w, nil
}

// Commit finalizes the transaction.
func (w *Writer) Commit() error {
	defer w.closeStmts()
	return w.tx.Commit()
}

// Rollback aborts the transaction.
func (w *Writer) Rollback() error {
	defer w.closeStmts()
	return w.tx.Rollback()
}

func (w *Writer) closeStmts() {
	for _, s := range []*sql.Stmt{w.insertConfigItem, w.upsertNode, w.insertPlaceholder, w.insertEdge} {
		if s != nil {
			s.Close()
		}
	}
}

// WriteConfigItem inserts a row into config_items. The insert is idempotent:
// a row with the same (resource, capture_time) — i.e., the same ConfigItemID
// — is silently ignored. Different capture times for the same resource
// produce distinct rows, preserving history.
func (w *Writer) WriteConfigItem(item model.ConfigItem) error {
	itemID := ConfigItemID(item)

	var relsJSON []byte
	if len(item.Relationships) > 0 {
		b, err := json.Marshal(item.Relationships)
		if err != nil {
			return fmt.Errorf("marshal relationships: %w", err)
		}
		relsJSON = b
	}

	_, err := w.insertConfigItem.Exec(
		itemID,
		item.AccountID,
		item.AwsRegion,
		item.ResourceType,
		item.ResourceID,
		nullIfEmpty(item.ResourceName),
		nullIfEmpty(item.ARN),
		item.ConfigurationItemCaptureTime,
		nullIfEmptyBytes(item.Configuration),
		nullIfEmptyBytes(relsJSON),
		nullIfEmptyBytes(item.Tags),
		nullIfEmptyBytes(item.Raw),
	)
	if err != nil {
		return fmt.Errorf("insert config_item %s: %w", itemID, err)
	}
	return nil
}

// WriteNode inserts a node. If Properties["placeholder"] is true the write is
// a "do nothing on conflict" insert so it never clobbers a real node.
func (w *Writer) WriteNode(n model.Node) error {
	propsJSON, err := json.Marshal(n.Properties)
	if err != nil {
		return fmt.Errorf("marshal properties: %w", err)
	}
	stmt := w.upsertNode
	if isPlaceholder(n) {
		stmt = w.insertPlaceholder
	}
	if _, err := stmt.Exec(
		n.NodeID,
		n.AccountID,
		n.AwsRegion,
		n.ResourceType,
		n.ResourceID,
		nullIfEmpty(n.ResourceName),
		nullIfEmpty(n.ARN),
		n.Label,
		string(propsJSON),
	); err != nil {
		return fmt.Errorf("insert node %s: %w", n.NodeID, err)
	}
	return nil
}

// WriteEdge inserts an edge (no-op if edge_id already exists).
func (w *Writer) WriteEdge(e model.Edge) error {
	propsJSON, err := json.Marshal(e.Properties)
	if err != nil {
		return fmt.Errorf("marshal properties: %w", err)
	}
	if _, err := w.insertEdge.Exec(
		e.EdgeID,
		e.SourceNodeID,
		e.TargetNodeID,
		e.RelationshipName,
		e.SourceType,
		e.TargetType,
		string(propsJSON),
	); err != nil {
		return fmt.Errorf("insert edge %s: %w", e.EdgeID, err)
	}
	return nil
}

func isPlaceholder(n model.Node) bool {
	if n.Properties == nil {
		return false
	}
	v, ok := n.Properties["placeholder"]
	if !ok {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullIfEmptyBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// ----- read helpers -----

// ListNodes returns all nodes; if resourceTypes is non-empty the result is filtered.
func (s *Store) ListNodes(ctx context.Context, resourceTypes []string) ([]model.Node, error) {
	q := `SELECT node_id, account_id, aws_region, resource_type, resource_id,
            COALESCE(resource_name, ''), COALESCE(arn, ''), label, COALESCE(properties_json, '{}')
          FROM graph_nodes`
	args := []any{}
	if len(resourceTypes) > 0 {
		q += " WHERE resource_type IN (" + placeholders(len(resourceTypes)) + ")"
		for _, t := range resourceTypes {
			args = append(args, t)
		}
	}
	q += " ORDER BY resource_type, resource_id"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Node
	for rows.Next() {
		var n model.Node
		var propsJSON string
		if err := rows.Scan(&n.NodeID, &n.AccountID, &n.AwsRegion, &n.ResourceType, &n.ResourceID, &n.ResourceName, &n.ARN, &n.Label, &propsJSON); err != nil {
			return nil, err
		}
		if propsJSON != "" {
			_ = json.Unmarshal([]byte(propsJSON), &n.Properties)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ListEdgesBetween returns edges where BOTH endpoints' resource_type are within types.
// If types is empty, returns all edges.
func (s *Store) ListEdgesBetween(ctx context.Context, types []string) ([]model.Edge, error) {
	q := `SELECT edge_id, source_node_id, target_node_id, relationship_name,
            source_resource_type, target_resource_type, COALESCE(properties_json, '{}')
          FROM graph_edges`
	args := []any{}
	if len(types) > 0 {
		ph := placeholders(len(types))
		q += " WHERE source_resource_type IN (" + ph + ") AND target_resource_type IN (" + ph + ")"
		for _, t := range types {
			args = append(args, t)
		}
		for _, t := range types {
			args = append(args, t)
		}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Edge
	for rows.Next() {
		var e model.Edge
		var propsJSON string
		if err := rows.Scan(&e.EdgeID, &e.SourceNodeID, &e.TargetNodeID, &e.RelationshipName, &e.SourceType, &e.TargetType, &propsJSON); err != nil {
			return nil, err
		}
		if propsJSON != "" {
			_ = json.Unmarshal([]byte(propsJSON), &e.Properties)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// FindNodesByResourceID returns all nodes whose resource_id matches.
// Useful when callers don't yet know the account/region.
func (s *Store) FindNodesByResourceID(ctx context.Context, resourceID string) ([]model.Node, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT node_id, account_id, aws_region, resource_type, resource_id,
            COALESCE(resource_name, ''), COALESCE(arn, ''), label, COALESCE(properties_json, '{}')
          FROM graph_nodes WHERE resource_id = ?`, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Node
	for rows.Next() {
		var n model.Node
		var propsJSON string
		if err := rows.Scan(&n.NodeID, &n.AccountID, &n.AwsRegion, &n.ResourceType, &n.ResourceID, &n.ResourceName, &n.ARN, &n.Label, &propsJSON); err != nil {
			return nil, err
		}
		if propsJSON != "" {
			_ = json.Unmarshal([]byte(propsJSON), &n.Properties)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// NeighborEdges returns all edges that touch any of the given node ids (in either direction).
func (s *Store) NeighborEdges(ctx context.Context, nodeIDs []string) ([]model.Edge, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	ph := placeholders(len(nodeIDs))
	q := `SELECT edge_id, source_node_id, target_node_id, relationship_name,
            source_resource_type, target_resource_type, COALESCE(properties_json, '{}')
          FROM graph_edges WHERE source_node_id IN (` + ph + `) OR target_node_id IN (` + ph + `)`
	args := make([]any, 0, len(nodeIDs)*2)
	for _, n := range nodeIDs {
		args = append(args, n)
	}
	for _, n := range nodeIDs {
		args = append(args, n)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Edge
	for rows.Next() {
		var e model.Edge
		var propsJSON string
		if err := rows.Scan(&e.EdgeID, &e.SourceNodeID, &e.TargetNodeID, &e.RelationshipName, &e.SourceType, &e.TargetType, &propsJSON); err != nil {
			return nil, err
		}
		if propsJSON != "" {
			_ = json.Unmarshal([]byte(propsJSON), &e.Properties)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetNodes returns nodes whose node_id is in the given set.
func (s *Store) GetNodes(ctx context.Context, nodeIDs []string) ([]model.Node, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	q := `SELECT node_id, account_id, aws_region, resource_type, resource_id,
            COALESCE(resource_name, ''), COALESCE(arn, ''), label, COALESCE(properties_json, '{}')
          FROM graph_nodes WHERE node_id IN (` + placeholders(len(nodeIDs)) + `)`
	args := make([]any, 0, len(nodeIDs))
	for _, n := range nodeIDs {
		args = append(args, n)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Node
	for rows.Next() {
		var n model.Node
		var propsJSON string
		if err := rows.Scan(&n.NodeID, &n.AccountID, &n.AwsRegion, &n.ResourceType, &n.ResourceID, &n.ResourceName, &n.ARN, &n.Label, &propsJSON); err != nil {
			return nil, err
		}
		if propsJSON != "" {
			_ = json.Unmarshal([]byte(propsJSON), &n.Properties)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}
