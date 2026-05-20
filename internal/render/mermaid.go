package render

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"clew/internal/model"
)

// VpcViewTypes lists the resource types that participate in the VPC view.
var VpcViewTypes = []string{
	"AWS::EC2::VPC",
	"AWS::EC2::Subnet",
	"AWS::EC2::RouteTable",
	"AWS::EC2::InternetGateway",
	"AWS::EC2::NatGateway",
	"AWS::EC2::SecurityGroup",
	"AWS::EC2::NetworkInterface",
	"AWS::EC2::Instance",
	"AWS::ElasticLoadBalancingV2::LoadBalancer",
	"AWS::ElasticLoadBalancingV2::TargetGroup",
	"AWS::RDS::DBInstance",
	"AWS::Lambda::Function",
}

// Options controls Mermaid rendering details.
type Options struct {
	// WithEdgeLabels emits `n1 -->|relationship| n2` instead of `n1 --> n2`.
	WithEdgeLabels bool
	// NoFence skips the surrounding ```mermaid code fence.
	NoFence bool
}

// Mermaid writes a `graph TD` for the given nodes and edges. Nodes referenced
// by edges but not present in the nodes slice are skipped.
func Mermaid(w io.Writer, nodes []model.Node, edges []model.Edge, opts Options) error {
	// Deterministic ordering for stable output.
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].ResourceType != nodes[j].ResourceType {
			return nodes[i].ResourceType < nodes[j].ResourceType
		}
		return nodes[i].ResourceID < nodes[j].ResourceID
	})
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].SourceNodeID != edges[j].SourceNodeID {
			return edges[i].SourceNodeID < edges[j].SourceNodeID
		}
		if edges[i].TargetNodeID != edges[j].TargetNodeID {
			return edges[i].TargetNodeID < edges[j].TargetNodeID
		}
		return edges[i].RelationshipName < edges[j].RelationshipName
	})

	shortID := make(map[string]string, len(nodes))
	for i, n := range nodes {
		shortID[n.NodeID] = fmt.Sprintf("n%d", i+1)
	}

	var sb strings.Builder
	if !opts.NoFence {
		sb.WriteString("```mermaid\n")
	}
	sb.WriteString("graph TD\n")
	for _, n := range nodes {
		fmt.Fprintf(&sb, "  %s[\"%s\"]\n", shortID[n.NodeID], escapeLabel(n.Label))
	}
	if len(edges) > 0 {
		sb.WriteString("\n")
	}
	// Deduplicate identical (source,target) lines when not labelling edges to
	// keep the output tidy.
	seen := make(map[string]struct{}, len(edges))
	for _, e := range edges {
		sid, ok1 := shortID[e.SourceNodeID]
		tid, ok2 := shortID[e.TargetNodeID]
		if !ok1 || !ok2 {
			continue
		}
		if opts.WithEdgeLabels {
			fmt.Fprintf(&sb, "  %s -->|%s| %s\n", sid, escapeLabel(e.RelationshipName), tid)
			continue
		}
		key := sid + "->" + tid
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		fmt.Fprintf(&sb, "  %s --> %s\n", sid, tid)
	}
	if !opts.NoFence {
		sb.WriteString("```\n")
	}

	_, err := io.WriteString(w, sb.String())
	return err
}

// escapeLabel keeps Mermaid-quoted labels safe. Real newlines in the stored
// label are turned into the two-character `\n` sequence Mermaid uses for line
// breaks inside a quoted node label.
func escapeLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}
