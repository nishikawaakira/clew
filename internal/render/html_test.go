package render

import (
	"bytes"
	"strings"
	"testing"

	"aws-config-graph/internal/model"
)

func TestHTMLContainsCoreScaffolding(t *testing.T) {
	nodes := []model.Node{
		{NodeID: "a", ResourceType: "AWS::EC2::VPC", ResourceID: "vpc-aaa", Label: "VPC\nvpc-aaa"},
		{NodeID: "b", ResourceType: "AWS::EC2::Subnet", ResourceID: "subnet-1", Label: "Subnet\nsubnet-1"},
	}
	edges := []model.Edge{
		{EdgeID: "e1", SourceNodeID: "b", TargetNodeID: "a", RelationshipName: "In VPC"},
	}
	var buf bytes.Buffer
	if err := HTML(&buf, nodes, edges, HTMLOptions{}); err != nil {
		t.Fatalf("HTML: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"<!doctype html>",
		"vis-network",
		"const DATA =",
		`"id":"n1"`,
		`"id":"n2"`,
		`"from":"n1"`,
		`"to":"n2"`,
		"aws-config-graph",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in HTML output", want)
		}
	}
}

func TestHTMLEscapesScriptTagInLabels(t *testing.T) {
	// Resource name with a </script> trying to break out of the inline <script>.
	nodes := []model.Node{
		{NodeID: "a", ResourceType: "AWS::EC2::VPC", ResourceID: "vpc-aaa",
			Label: `VPC</script><script>alert(1)</script>`, ResourceName: "evil"},
	}
	var buf bytes.Buffer
	if err := HTML(&buf, nodes, nil, HTMLOptions{}); err != nil {
		t.Fatalf("HTML: %v", err)
	}
	out := buf.String()
	// The literal `</script>` from the label must NOT appear unescaped between
	// the data-bearing script open and the next </script> close, otherwise it
	// would terminate the script tag early.
	idx := strings.Index(out, "const DATA =")
	if idx < 0 {
		t.Fatal("const DATA not found")
	}
	// Up to the next </script>, the substring must not contain the lowercase
	// `</script>` literal — JSON HTML escaping should have turned `<` into <.
	end := strings.Index(out[idx:], "</script>")
	if end < 0 {
		t.Fatal("script close not found")
	}
	body := out[idx : idx+end]
	if strings.Contains(body, "</script>") {
		t.Fatalf("found unescaped </script> inside data script body")
	}
}

func TestHTMLPlaceholderStyling(t *testing.T) {
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
	if !strings.Contains(out, `"borderDashes":[4,4]`) {
		t.Fatalf("placeholder borderDashes missing in payload")
	}
}
