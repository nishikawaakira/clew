package graph

import (
	"encoding/json"
	"testing"

	"aws-config-graph/internal/model"
)

func makeItem(t *testing.T, resourceType, resourceID string, cfg map[string]any, rels []model.Relationship) model.ConfigItem {
	t.Helper()
	cfgRaw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal cfg: %v", err)
	}
	return model.ConfigItem{
		AccountID:     "123",
		AwsRegion:     "ap-northeast-1",
		ResourceType:  resourceType,
		ResourceID:    resourceID,
		Configuration: cfgRaw,
		Relationships: rels,
	}
}

func TestBuildRealNodeAndRelationshipEdges(t *testing.T) {
	rels := []model.Relationship{
		{ResourceType: "AWS::EC2::VPC", ResourceID: "vpc-1", RelationshipName: "Is contained in Vpc"},
	}
	item := makeItem(t, "AWS::EC2::Subnet", "subnet-1", map[string]any{"vpcId": "vpc-1"}, rels)

	got := Build(item)
	if got.Node.NodeID != "123:ap-northeast-1:AWS::EC2::Subnet:subnet-1" {
		t.Fatalf("unexpected node_id %q", got.Node.NodeID)
	}
	if got.Node.Label != "Subnet\nsubnet-1" {
		t.Fatalf("unexpected label %q", got.Node.Label)
	}
	if len(got.Edges) != 2 {
		t.Fatalf("want 2 edges (relationship + config), got %d", len(got.Edges))
	}
	for _, e := range got.Edges {
		if e.TargetNodeID != "123:ap-northeast-1:AWS::EC2::VPC:vpc-1" {
			t.Fatalf("unexpected target %q", e.TargetNodeID)
		}
	}
	if len(got.PlaceholderNodes) != 2 {
		t.Fatalf("want 2 placeholder candidates, got %d", len(got.PlaceholderNodes))
	}
	for _, p := range got.PlaceholderNodes {
		if v, ok := p.Properties["placeholder"]; !ok || v != true {
			t.Fatalf("placeholder flag missing on %+v", p)
		}
	}
}

func TestRouteTableExtractsGatewayEdges(t *testing.T) {
	cfg := map[string]any{
		"vpcId": "vpc-1",
		"routes": []any{
			map[string]any{"gatewayId": "igw-1"},
			map[string]any{"gatewayId": "local"},
			map[string]any{"natGatewayId": "nat-1"},
			map[string]any{"networkInterfaceId": "eni-1"},
		},
	}
	item := makeItem(t, "AWS::EC2::RouteTable", "rtb-1", cfg, nil)
	got := Build(item)

	targets := map[string]string{}
	for _, e := range got.Edges {
		targets[e.TargetType] = e.TargetNodeID
	}
	for _, want := range []string{"AWS::EC2::VPC", "AWS::EC2::InternetGateway", "AWS::EC2::NatGateway", "AWS::EC2::NetworkInterface"} {
		if _, ok := targets[want]; !ok {
			t.Fatalf("missing edge to %s; have %v", want, targets)
		}
	}
	// `local` must not appear as an edge.
	for _, e := range got.Edges {
		if e.TargetNodeID == "123:ap-northeast-1::local" || e.TargetType == "" {
			t.Fatalf("local route should be skipped, got %+v", e)
		}
	}
}

func TestEdgeIDIsStable(t *testing.T) {
	a := EdgeID("src", "rel", "dst")
	b := EdgeID("src", "rel", "dst")
	if a != b {
		t.Fatalf("expected deterministic edge id")
	}
	c := EdgeID("src", "rel2", "dst")
	if a == c {
		t.Fatalf("expected different edge ids for different relationship names")
	}
}

func TestShortTypeFallback(t *testing.T) {
	if got := ShortType("AWS::Custom::Widget"); got != "Widget" {
		t.Fatalf("fallback ShortType got %q", got)
	}
	if got := ShortType("AWS::EC2::VPC"); got != "VPC" {
		t.Fatalf("mapped ShortType got %q", got)
	}
}
