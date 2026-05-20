package render

import (
	"bytes"
	"strings"
	"testing"

	"clew/internal/model"
)

func TestMermaidOutputsFenceAndArrows(t *testing.T) {
	nodes := []model.Node{
		{NodeID: "n:vpc", ResourceType: "AWS::EC2::VPC", ResourceID: "vpc-aaa", Label: "VPC\nvpc-aaa"},
		{NodeID: "n:subnet", ResourceType: "AWS::EC2::Subnet", ResourceID: "subnet-1", Label: "Subnet\nsubnet-1"},
	}
	edges := []model.Edge{
		{EdgeID: "e1", SourceNodeID: "n:subnet", TargetNodeID: "n:vpc", RelationshipName: "In VPC"},
	}
	var buf bytes.Buffer
	if err := Mermaid(&buf, nodes, edges, Options{}); err != nil {
		t.Fatalf("Mermaid: %v", err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "```mermaid\n") {
		t.Fatalf("expected mermaid fence prefix, got %q", out)
	}
	if !strings.Contains(out, "graph TD\n") {
		t.Fatalf("missing graph TD header: %q", out)
	}
	if !strings.Contains(out, "\"VPC\\nvpc-aaa\"") {
		t.Fatalf("VPC label missing: %q", out)
	}
	if !strings.Contains(out, " --> ") {
		t.Fatalf("expected an arrow in output: %q", out)
	}
}

func TestMermaidWithEdgeLabels(t *testing.T) {
	nodes := []model.Node{
		{NodeID: "a", ResourceType: "AWS::EC2::VPC", ResourceID: "vpc-aaa", Label: "VPC\nvpc-aaa"},
		{NodeID: "b", ResourceType: "AWS::EC2::Subnet", ResourceID: "subnet-1", Label: "Subnet\nsubnet-1"},
	}
	edges := []model.Edge{
		{EdgeID: "e1", SourceNodeID: "b", TargetNodeID: "a", RelationshipName: "In VPC"},
	}
	var buf bytes.Buffer
	if err := Mermaid(&buf, nodes, edges, Options{WithEdgeLabels: true}); err != nil {
		t.Fatalf("Mermaid: %v", err)
	}
	if !strings.Contains(buf.String(), "-->|In VPC|") {
		t.Fatalf("expected edge label, got %q", buf.String())
	}
}

func TestMermaidSkipsEdgesToUnknownNodes(t *testing.T) {
	nodes := []model.Node{
		{NodeID: "a", ResourceType: "AWS::EC2::VPC", ResourceID: "vpc-aaa", Label: "VPC\nvpc-aaa"},
	}
	edges := []model.Edge{
		{EdgeID: "e1", SourceNodeID: "a", TargetNodeID: "missing", RelationshipName: "?"},
	}
	var buf bytes.Buffer
	if err := Mermaid(&buf, nodes, edges, Options{}); err != nil {
		t.Fatalf("Mermaid: %v", err)
	}
	if strings.Contains(buf.String(), " --> ") {
		t.Fatalf("expected no arrows when target not in node list: %q", buf.String())
	}
}
