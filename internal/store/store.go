package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	_ "github.com/marcboeker/go-duckdb"

	"clew/internal/model"
)

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

func (s *Store) init() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS config_items (
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
        )`,
		`CREATE TABLE IF NOT EXISTS graph_nodes (
            node_id TEXT PRIMARY KEY,
            account_id TEXT,
            aws_region TEXT,
            resource_type TEXT,
            resource_id TEXT,
            resource_name TEXT,
            arn TEXT,
            label TEXT,
            properties_json TEXT
        )`,
		`CREATE TABLE IF NOT EXISTS graph_edges (
            edge_id TEXT PRIMARY KEY,
            source_node_id TEXT,
            target_node_id TEXT,
            relationship_name TEXT,
            source_resource_type TEXT,
            target_resource_type TEXT,
            properties_json TEXT
        )`,
	}
	for _, sch := range stmts {
		if _, err := s.db.Exec(sch); err != nil {
			return fmt.Errorf("init schema: %w", err)
		}
	}
	return nil
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
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`
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

// WriteConfigItem inserts a row into config_items.
func (w *Writer) WriteConfigItem(item model.ConfigItem) error {
	itemID := fmt.Sprintf("%s:%s:%s:%s", item.AccountID, item.AwsRegion, item.ResourceType, item.ResourceID)

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
