package render

import (
	"bytes"
	"strings"
	"testing"

	"clew/internal/model"
)

func TestHTMLContainsCytoscapeScaffolding(t *testing.T) {
	nodes := []model.Node{
		{NodeID: "a", ResourceType: "AWS::EC2::VPC", ResourceID: "vpc-aaa", Label: "VPC\nvpc-aaa"},
		{NodeID: "b", ResourceType: "AWS::EC2::Subnet", ResourceID: "subnet-1", Label: "Subnet\nsubnet-1"},
	}
	edges := []model.Edge{
		{EdgeID: "e1", SourceNodeID: "b", TargetNodeID: "a", RelationshipName: "In VPC",
			SourceType: "AWS::EC2::Subnet", TargetType: "AWS::EC2::VPC"},
	}
	var buf bytes.Buffer
	if err := HTML(&buf, nodes, edges, HTMLOptions{}); err != nil {
		t.Fatalf("HTML: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"<!doctype html>",
		"cytoscape@",
		"cytoscape-dagre@",
		"const DATA =",
		`"resourceType":"AWS::EC2::VPC"`,
		`"resourceType":"AWS::EC2::Subnet"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in HTML output", want)
		}
	}
}

func TestHTMLSubnetGetsVPCParent(t *testing.T) {
	nodes := []model.Node{
		{NodeID: "vpc", ResourceType: "AWS::EC2::VPC", ResourceID: "vpc-aaa", Label: "VPC\nvpc-aaa"},
		{NodeID: "subnet", ResourceType: "AWS::EC2::Subnet", ResourceID: "subnet-1", Label: "Subnet\nsubnet-1"},
	}
	edges := []model.Edge{
		{EdgeID: "e1", SourceNodeID: "subnet", TargetNodeID: "vpc", RelationshipName: "In VPC",
			SourceType: "AWS::EC2::Subnet", TargetType: "AWS::EC2::VPC"},
	}
	payload := buildCytoscapePayload(nodes, edges)

	// Find the subnet node in the payload.
	var subnetNode *cyNode
	var vpcNode *cyNode
	for i := range payload.Nodes {
		switch payload.Nodes[i].Data.ResourceType {
		case "AWS::EC2::Subnet":
			subnetNode = &payload.Nodes[i]
		case "AWS::EC2::VPC":
			vpcNode = &payload.Nodes[i]
		}
	}
	if subnetNode == nil || vpcNode == nil {
		t.Fatalf("missing nodes in payload: %+v", payload.Nodes)
	}
	if subnetNode.Data.Parent != vpcNode.Data.ID {
		t.Fatalf("subnet.parent = %q, want %q", subnetNode.Data.Parent, vpcNode.Data.ID)
	}
	if !vpcNode.Data.IsContainer {
		t.Fatalf("VPC should be marked isContainer")
	}
}

func TestHTMLInstancePrefersSubnetOverVPC(t *testing.T) {
	nodes := []model.Node{
		{NodeID: "vpc", ResourceType: "AWS::EC2::VPC", ResourceID: "vpc-aaa", Label: "VPC\nvpc-aaa"},
		{NodeID: "subnet", ResourceType: "AWS::EC2::Subnet", ResourceID: "subnet-1", Label: "Subnet\nsubnet-1"},
		{NodeID: "inst", ResourceType: "AWS::EC2::Instance", ResourceID: "i-1", Label: "EC2\ni-1"},
	}
	edges := []model.Edge{
		{EdgeID: "1", SourceNodeID: "inst", TargetNodeID: "subnet", RelationshipName: "In subnet"},
		{EdgeID: "2", SourceNodeID: "inst", TargetNodeID: "vpc", RelationshipName: "In VPC"},
		{EdgeID: "3", SourceNodeID: "subnet", TargetNodeID: "vpc", RelationshipName: "In VPC"},
	}
	payload := buildCytoscapePayload(nodes, edges)

	var inst *cyNode
	idByLogical := map[string]string{}
	for i := range payload.Nodes {
		idByLogical[payload.Nodes[i].Data.ResourceType] = payload.Nodes[i].Data.ID
		if payload.Nodes[i].Data.ResourceType == "AWS::EC2::Instance" {
			inst = &payload.Nodes[i]
		}
	}
	if inst == nil {
		t.Fatalf("instance missing in payload")
	}
	if inst.Data.Parent != idByLogical["AWS::EC2::Subnet"] {
		t.Fatalf("instance parent should be subnet, got %q (subnet=%q vpc=%q)",
			inst.Data.Parent, idByLogical["AWS::EC2::Subnet"], idByLogical["AWS::EC2::VPC"])
	}
}

func TestHTMLContainmentEdgesAreSuppressed(t *testing.T) {
	nodes := []model.Node{
		{NodeID: "vpc", ResourceType: "AWS::EC2::VPC", ResourceID: "vpc-aaa", Label: "VPC\nvpc-aaa"},
		{NodeID: "subnet", ResourceType: "AWS::EC2::Subnet", ResourceID: "subnet-1", Label: "Subnet\nsubnet-1"},
		{NodeID: "sg", ResourceType: "AWS::EC2::SecurityGroup", ResourceID: "sg-1", Label: "SecurityGroup\nsg-1"},
	}
	edges := []model.Edge{
		// containment — should disappear:
		{EdgeID: "1", SourceNodeID: "subnet", TargetNodeID: "vpc", RelationshipName: "In VPC"},
		{EdgeID: "2", SourceNodeID: "sg", TargetNodeID: "vpc", RelationshipName: "In VPC"},
		// non-containment — should survive (sg referencing another sg-like):
		{EdgeID: "3", SourceNodeID: "sg", TargetNodeID: "subnet", RelationshipName: "Uses security group"},
	}
	payload := buildCytoscapePayload(nodes, edges)

	if len(payload.Edges) != 1 {
		t.Fatalf("expected 1 edge after containment suppression, got %d: %+v", len(payload.Edges), payload.Edges)
	}
	if payload.Edges[0].Data.Label != "Uses security group" {
		t.Fatalf("surviving edge should be 'Uses security group', got %q", payload.Edges[0].Data.Label)
	}
}

func TestHTMLEscapesScriptTagInLabels(t *testing.T) {
	nodes := []model.Node{
		{NodeID: "a", ResourceType: "AWS::EC2::VPC", ResourceID: "vpc-aaa",
			Label: `VPC</script><script>alert(1)</script>`},
	}
	var buf bytes.Buffer
	if err := HTML(&buf, nodes, nil, HTMLOptions{}); err != nil {
		t.Fatalf("HTML: %v", err)
	}
	out := buf.String()
	idx := strings.Index(out, "const DATA =")
	if idx < 0 {
		t.Fatal("const DATA not found")
	}
	end := strings.Index(out[idx:], "</script>")
	if end < 0 {
		t.Fatal("script close not found")
	}
	body := out[idx : idx+end]
	if strings.Contains(body, "</script>") {
		t.Fatalf("found unescaped </script> inside data script body")
	}
}

func TestHTMLPlaceholderClass(t *testing.T) {
	nodes := []model.Node{
		{NodeID: "a", ResourceType: "AWS::EC2::SecurityGroup", ResourceID: "sg-zzz",
			Label: "SecurityGroup\nsg-zzz", Properties: map[string]any{"placeholder": true}},
	}
	var buf bytes.Buffer
	if err := HTML(&buf, nodes, nil, HTMLOptions{}); err != nil {
		t.Fatalf("HTML: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"placeholder":true`) {
		t.Fatalf("placeholder flag missing in payload: %s", out)
	}
	if !strings.Contains(out, `"classes":"rt-aws-ec2-securitygroup placeholder"`) {
		t.Fatalf("placeholder class missing in payload, got: %s", out)
	}
}
