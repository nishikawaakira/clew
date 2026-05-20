package render

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/goccy/go-graphviz"
	"github.com/goccy/go-graphviz/cgraph"

	"aws-config-graph/internal/model"
)

// GraphvizFormat enumerates the output formats the bundled WASM Graphviz can
// produce. The values map 1:1 to the underlying library constants.
type GraphvizFormat string

const (
	GraphvizSVG GraphvizFormat = "svg"
	GraphvizPNG GraphvizFormat = "png"
	GraphvizJPG GraphvizFormat = "jpg"
	GraphvizDOT GraphvizFormat = "dot"
)

// GraphvizOptions controls Graphviz rendering.
type GraphvizOptions struct {
	Format         GraphvizFormat
	WithEdgeLabels bool
	// Layout is the Graphviz layout engine (dot/neato/fdp/circo/...).
	// Empty string falls back to "dot" which is best for hierarchical
	// VPC views.
	Layout string
}

// Graphviz renders the given nodes and edges into the chosen format using the
// pure-Go (WASM) Graphviz bundled by goccy/go-graphviz. No system `graphviz`
// install or external `dot` binary is required.
func Graphviz(ctx context.Context, w io.Writer, nodes []model.Node, edges []model.Edge, opts GraphvizOptions) error {
	if opts.Format == "" {
		opts.Format = GraphvizSVG
	}

	g, err := graphviz.New(ctx)
	if err != nil {
		return fmt.Errorf("graphviz init: %w", err)
	}
	defer g.Close()

	if opts.Layout != "" {
		g.SetLayout(graphviz.Layout(opts.Layout))
	}

	graph, err := g.Graph()
	if err != nil {
		return fmt.Errorf("graphviz graph: %w", err)
	}
	defer graph.Close()

	graph.SetRankDir(cgraph.TBRank)

	// Deterministic ordering so the resulting DOT/SVG byte stream is stable.
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

	gNodes := make(map[string]*cgraph.Node, len(nodes))
	for i, n := range nodes {
		name := fmt.Sprintf("n%d", i+1)
		gn, err := graph.CreateNodeByName(name)
		if err != nil {
			return fmt.Errorf("create node: %w", err)
		}
		style := styleFor(n.ResourceType)
		gn.SetLabel(n.Label)
		gn.SetShape(style.Shape)
		gn.SetStyle(cgraph.FilledNodeStyle)
		gn.SetFillColor(style.FillColor)
		gn.SetFontName("Helvetica")
		gn.SetFontSize(10)
		if isPlaceholder(n) {
			// Show placeholder targets as a dashed outline so they're easy
			// to spot in the rendered diagram.
			gn.SetStyle(cgraph.NodeStyle("filled,dashed"))
			gn.SetFillColor("#f5f5f5")
		}
		gNodes[n.NodeID] = gn
	}

	// Deduplicate identical (source,target) edges when not labelling.
	seen := make(map[string]struct{}, len(edges))
	for i, e := range edges {
		src, ok1 := gNodes[e.SourceNodeID]
		dst, ok2 := gNodes[e.TargetNodeID]
		if !ok1 || !ok2 {
			continue
		}
		if !opts.WithEdgeLabels {
			key := e.SourceNodeID + "->" + e.TargetNodeID
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
		}
		ge, err := graph.CreateEdgeByName(fmt.Sprintf("e%d", i+1), src, dst)
		if err != nil {
			return fmt.Errorf("create edge: %w", err)
		}
		if opts.WithEdgeLabels && e.RelationshipName != "" {
			ge.SetLabel(e.RelationshipName)
			ge.SetFontSize(8)
			ge.SetFontName("Helvetica")
		}
	}

	return g.Render(ctx, graph, graphviz.Format(opts.Format), w)
}

type nodeStyle struct {
	Shape     cgraph.Shape
	FillColor string
}

// styleFor returns a Shape + FillColor combo for the given AWS resource type.
// Unknown types fall back to a neutral rectangle.
func styleFor(resourceType string) nodeStyle {
	switch resourceType {
	case "AWS::EC2::VPC":
		return nodeStyle{Shape: cgraph.BoxShape, FillColor: "#cfe2f3"}
	case "AWS::EC2::Subnet":
		return nodeStyle{Shape: cgraph.BoxShape, FillColor: "#d9ead3"}
	case "AWS::EC2::RouteTable":
		return nodeStyle{Shape: cgraph.NoteShape, FillColor: "#fff2cc"}
	case "AWS::EC2::InternetGateway":
		return nodeStyle{Shape: cgraph.TrapeziumShape, FillColor: "#b6d7a8"}
	case "AWS::EC2::NatGateway":
		return nodeStyle{Shape: cgraph.InvTrapeziumShape, FillColor: "#a2c4c9"}
	case "AWS::EC2::SecurityGroup":
		return nodeStyle{Shape: cgraph.HexagonShape, FillColor: "#f4cccc"}
	case "AWS::EC2::NetworkInterface":
		return nodeStyle{Shape: cgraph.OvalShape, FillColor: "#ead1dc"}
	case "AWS::EC2::Instance":
		return nodeStyle{Shape: cgraph.Box3DShape, FillColor: "#ffe599"}
	case "AWS::ElasticLoadBalancingV2::LoadBalancer":
		return nodeStyle{Shape: cgraph.EllipseShape, FillColor: "#ea9999"}
	case "AWS::ElasticLoadBalancingV2::TargetGroup":
		return nodeStyle{Shape: cgraph.EllipseShape, FillColor: "#f9cb9c"}
	case "AWS::RDS::DBInstance":
		return nodeStyle{Shape: cgraph.CylinderShape, FillColor: "#9fc5e8"}
	case "AWS::Lambda::Function":
		return nodeStyle{Shape: cgraph.HexagonShape, FillColor: "#d5a6bd"}
	default:
		return nodeStyle{Shape: cgraph.RectShape, FillColor: "#eeeeee"}
	}
}

func isPlaceholder(n model.Node) bool {
	if n.Properties == nil {
		return false
	}
	if v, ok := n.Properties["placeholder"]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}
