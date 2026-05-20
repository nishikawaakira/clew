package graph

import (
	"encoding/json"

	"aws-config-graph/internal/model"
)

// Built is the result of converting a single ConfigItem into graph artifacts.
type Built struct {
	Node             model.Node
	Edges            []model.Edge
	PlaceholderNodes []model.Node
}

// Build converts one configurationItem into a real node plus the edges/placeholders
// implied by its relationships and (where supported) its configuration body.
func Build(item model.ConfigItem) Built {
	sourceID := NodeID(item.AccountID, item.AwsRegion, item.ResourceType, item.ResourceID)

	properties := map[string]any{}
	if len(item.Configuration) > 0 {
		var cfg any
		if err := json.Unmarshal(item.Configuration, &cfg); err == nil {
			properties["configuration"] = cfg
		}
	}
	if item.ResourceName != "" {
		properties["resourceName"] = item.ResourceName
	}

	node := model.Node{
		NodeID:       sourceID,
		AccountID:    item.AccountID,
		AwsRegion:    item.AwsRegion,
		ResourceType: item.ResourceType,
		ResourceID:   item.ResourceID,
		ResourceName: item.ResourceName,
		ARN:          item.ARN,
		Label:        MakeLabel(item.ResourceType, item.ResourceID),
		Properties:   properties,
	}

	res := Built{Node: node}

	for _, rel := range item.Relationships {
		if rel.ResourceID == "" || rel.ResourceType == "" {
			continue
		}
		targetID := NodeID(item.AccountID, item.AwsRegion, rel.ResourceType, rel.ResourceID)
		res.Edges = append(res.Edges, model.Edge{
			EdgeID:           EdgeID(sourceID, rel.RelationshipName, targetID),
			SourceNodeID:     sourceID,
			TargetNodeID:     targetID,
			RelationshipName: rel.RelationshipName,
			SourceType:       item.ResourceType,
			TargetType:       rel.ResourceType,
			Properties:       map[string]any{"origin": "relationships"},
		})
		res.PlaceholderNodes = append(res.PlaceholderNodes, placeholder(item.AccountID, item.AwsRegion, rel.ResourceType, rel.ResourceID, rel.ResourceName))
	}

	for _, c := range ConfigEdges(item) {
		targetID := NodeID(item.AccountID, item.AwsRegion, c.TargetType, c.TargetID)
		res.Edges = append(res.Edges, model.Edge{
			EdgeID:           EdgeID(sourceID, c.RelationshipName, targetID),
			SourceNodeID:     sourceID,
			TargetNodeID:     targetID,
			RelationshipName: c.RelationshipName,
			SourceType:       item.ResourceType,
			TargetType:       c.TargetType,
			Properties:       map[string]any{"origin": "configuration"},
		})
		res.PlaceholderNodes = append(res.PlaceholderNodes, placeholder(item.AccountID, item.AwsRegion, c.TargetType, c.TargetID, ""))
	}

	return res
}

func placeholder(accountID, region, resourceType, resourceID, resourceName string) model.Node {
	return model.Node{
		NodeID:       NodeID(accountID, region, resourceType, resourceID),
		AccountID:    accountID,
		AwsRegion:    region,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		ResourceName: resourceName,
		Label:        MakeLabel(resourceType, resourceID),
		Properties:   map[string]any{"placeholder": true},
	}
}
