package render

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"clew/internal/model"
)

func sampleGraph() ([]model.Node, []model.Edge) {
	nodes := []model.Node{
		{NodeID: "n:vpc", ResourceType: "AWS::EC2::VPC", ResourceID: "vpc-aaa", Label: "VPC\nvpc-aaa"},
		{NodeID: "n:subnet", ResourceType: "AWS::EC2::Subnet", ResourceID: "subnet-1", Label: "Subnet\nsubnet-1"},
		{NodeID: "n:placeholder", ResourceType: "AWS::EC2::SecurityGroup", ResourceID: "sg-zzz",
			Label: "SecurityGroup\nsg-zzz", Properties: map[string]any{"placeholder": true}},
	}
	edges := []model.Edge{
		{EdgeID: "e1", SourceNodeID: "n:subnet", TargetNodeID: "n:vpc", RelationshipName: "In VPC"},
	}
	return nodes, edges
}

func TestGraphvizDOTOutput(t *testing.T) {
	nodes, edges := sampleGraph()
	var buf bytes.Buffer
	if err := Graphviz(context.Background(), &buf, nodes, edges, GraphvizOptions{Format: GraphvizDOT}); err != nil {
		t.Fatalf("Graphviz dot: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "digraph") {
		t.Fatalf("expected digraph header in dot output, got: %s", out)
	}
	if !strings.Contains(out, "VPC") || !strings.Contains(out, "vpc-aaa") {
		t.Fatalf("expected VPC label in dot output, got: %s", out)
	}
}

func TestGraphvizSVGOutput(t *testing.T) {
	nodes, edges := sampleGraph()
	var buf bytes.Buffer
	if err := Graphviz(context.Background(), &buf, nodes, edges, GraphvizOptions{Format: GraphvizSVG}); err != nil {
		t.Fatalf("Graphviz svg: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatalf("svg output is empty")
	}
	if !strings.Contains(buf.String(), "<svg") {
		t.Fatalf("expected <svg tag in output, got prefix: %q", buf.String()[:min(100, buf.Len())])
	}
}

func TestGraphvizSkipsEdgesToUnknownNodes(t *testing.T) {
	nodes := []model.Node{
		{NodeID: "a", ResourceType: "AWS::EC2::VPC", ResourceID: "vpc-aaa", Label: "VPC\nvpc-aaa"},
	}
	edges := []model.Edge{
		{EdgeID: "e1", SourceNodeID: "a", TargetNodeID: "missing", RelationshipName: "?"},
	}
	var buf bytes.Buffer
	if err := Graphviz(context.Background(), &buf, nodes, edges, GraphvizOptions{Format: GraphvizDOT}); err != nil {
		t.Fatalf("Graphviz: %v", err)
	}
	if strings.Contains(buf.String(), "->") {
		t.Fatalf("expected no edges in output when target unknown, got: %s", buf.String())
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
