package model

import (
	"encoding/json"
	"time"
)

// ConfigItem mirrors a single configurationItem entry inside an AWS Config snapshot.
type ConfigItem struct {
	AccountID                    string          `json:"accountId"`
	AwsRegion                    string          `json:"awsRegion"`
	ResourceType                 string          `json:"resourceType"`
	ResourceID                   string          `json:"resourceId"`
	ResourceName                 string          `json:"resourceName,omitempty"`
	ARN                          string          `json:"arn,omitempty"`
	ConfigurationItemCaptureTime time.Time       `json:"configurationItemCaptureTime"`
	Configuration                json.RawMessage `json:"configuration,omitempty"`
	Relationships                []Relationship  `json:"relationships,omitempty"`
	Tags                         json.RawMessage `json:"tags,omitempty"`

	// Raw is the original JSON for the whole item (populated during streaming reads).
	Raw json.RawMessage `json:"-"`
}

// Relationship is one entry of configurationItem.relationships.
type Relationship struct {
	ResourceType     string `json:"resourceType"`
	ResourceID       string `json:"resourceId"`
	ResourceName     string `json:"resourceName,omitempty"`
	RelationshipName string `json:"relationshipName"`
}

// Node is a row in graph_nodes.
type Node struct {
	NodeID       string
	AccountID    string
	AwsRegion    string
	ResourceType string
	ResourceID   string
	ResourceName string
	ARN          string
	Label        string
	Properties   map[string]any
}

// Edge is a row in graph_edges.
type Edge struct {
	EdgeID           string
	SourceNodeID     string
	TargetNodeID     string
	RelationshipName string
	SourceType       string
	TargetType       string
	Properties       map[string]any
}
