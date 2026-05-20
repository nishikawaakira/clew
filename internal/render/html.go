package render

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"sort"

	"aws-config-graph/internal/model"
)

//go:embed html_template.html
var htmlTemplateSrc string

// HTMLOptions controls the HTML render output.
type HTMLOptions struct {
	Title          string
	WithEdgeLabels bool
}

// HTML renders an interactive, self-contained HTML page that draws the graph
// with vis-network (loaded from a CDN). The page supports zoom/pan, toggling
// hierarchical layout vs. physics, click-to-inspect, and a legend derived from
// the resource types actually present.
func HTML(w io.Writer, nodes []model.Node, edges []model.Edge, opts HTMLOptions) error {
	if opts.Title == "" {
		opts.Title = "aws-config-graph — VPC view"
	}

	tmpl, err := template.New("page").Parse(htmlTemplateSrc)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	payload, err := buildVisPayload(nodes, edges)
	if err != nil {
		return err
	}

	// JSON encode with HTML escaping so `</script>` / `<` / `>` / `&` in
	// resource names can't break out of the embedded <script> tag.
	var jsonBuf bytes.Buffer
	enc := json.NewEncoder(&jsonBuf)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("marshal vis payload: %w", err)
	}

	return tmpl.Execute(w, struct {
		Title          string
		DataJSON       template.JS
		WithEdgeLabels template.JS
	}{
		Title:          opts.Title,
		DataJSON:       template.JS(bytes.TrimRight(jsonBuf.Bytes(), "\n")),
		WithEdgeLabels: boolJS(opts.WithEdgeLabels),
	})
}

func boolJS(b bool) template.JS {
	if b {
		return template.JS("true")
	}
	return template.JS("false")
}

// visNode is the JSON shape consumed by vis-network's DataSet.
type visNode struct {
	ID           string         `json:"id"`
	Label        string         `json:"label"`
	Shape        string         `json:"shape"`
	Color        visNodeColor   `json:"color"`
	Title        string         `json:"title,omitempty"`
	ResourceType string         `json:"resourceType"`
	ResourceID   string         `json:"resourceId"`
	AccountID    string         `json:"accountId"`
	AwsRegion    string         `json:"awsRegion"`
	ARN          string         `json:"arn,omitempty"`
	Placeholder  bool           `json:"placeholder"`
	BorderDashes []int          `json:"borderDashes,omitempty"`
	Font         map[string]any `json:"font,omitempty"`
}

type visNodeColor struct {
	Background string `json:"background"`
	Border     string `json:"border"`
}

type visEdge struct {
	ID    string `json:"id"`
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
}

type visPayload struct {
	Nodes []visNode `json:"nodes"`
	Edges []visEdge `json:"edges"`
}

func buildVisPayload(nodes []model.Node, edges []model.Edge) (visPayload, error) {
	// Deterministic output for diffing.
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

	// Short id map keeps DOM-friendly node references and shorter HTML.
	short := make(map[string]string, len(nodes))
	out := visPayload{Nodes: make([]visNode, 0, len(nodes)), Edges: make([]visEdge, 0, len(edges))}

	for i, n := range nodes {
		sid := fmt.Sprintf("n%d", i+1)
		short[n.NodeID] = sid

		style := visStyleFor(n.ResourceType)
		vn := visNode{
			ID:           sid,
			Label:        n.Label,
			Shape:        style.Shape,
			Color:        visNodeColor{Background: style.Background, Border: style.Border},
			ResourceType: n.ResourceType,
			ResourceID:   n.ResourceID,
			AccountID:    n.AccountID,
			AwsRegion:    n.AwsRegion,
			ARN:          n.ARN,
			Title:        n.ResourceType + " — " + n.ResourceID,
		}
		if isPlaceholderHTML(n) {
			vn.Placeholder = true
			vn.BorderDashes = []int{4, 4}
			vn.Color.Background = "#f5f5f5"
			vn.Color.Border = "#999"
			vn.Font = map[string]any{"color": "#777"}
		}
		out.Nodes = append(out.Nodes, vn)
	}

	// Edges: drop those pointing to nodes we don't have.
	seen := make(map[string]struct{}, len(edges))
	for i, e := range edges {
		s, ok1 := short[e.SourceNodeID]
		t, ok2 := short[e.TargetNodeID]
		if !ok1 || !ok2 {
			continue
		}
		// Deduplicate identical (src, dst, label) triples to keep visual noise down.
		key := s + "->" + t + "|" + e.RelationshipName
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out.Edges = append(out.Edges, visEdge{
			ID:    fmt.Sprintf("e%d", i+1),
			From:  s,
			To:    t,
			Label: e.RelationshipName,
		})
	}
	return out, nil
}

type htmlStyle struct {
	Shape      string
	Background string
	Border     string
}

// vis-network supports: ellipse, circle, database, box, text, diamond,
// dot, star, triangle, triangleDown, hexagon, square.
var visStyles = map[string]htmlStyle{
	"AWS::EC2::VPC":                             {Shape: "box", Background: "#cfe2f3", Border: "#3b82f6"},
	"AWS::EC2::Subnet":                          {Shape: "box", Background: "#d9ead3", Border: "#16a34a"},
	"AWS::EC2::RouteTable":                      {Shape: "box", Background: "#fff2cc", Border: "#ca8a04"},
	"AWS::EC2::InternetGateway":                 {Shape: "diamond", Background: "#b6d7a8", Border: "#22c55e"},
	"AWS::EC2::NatGateway":                      {Shape: "diamond", Background: "#a2c4c9", Border: "#0891b2"},
	"AWS::EC2::SecurityGroup":                   {Shape: "hexagon", Background: "#f4cccc", Border: "#dc2626"},
	"AWS::EC2::NetworkInterface":                {Shape: "ellipse", Background: "#ead1dc", Border: "#a855f7"},
	"AWS::EC2::Instance":                        {Shape: "box", Background: "#ffe599", Border: "#eab308"},
	"AWS::ElasticLoadBalancingV2::LoadBalancer": {Shape: "ellipse", Background: "#ea9999", Border: "#ef4444"},
	"AWS::ElasticLoadBalancingV2::TargetGroup":  {Shape: "ellipse", Background: "#f9cb9c", Border: "#f97316"},
	"AWS::RDS::DBInstance":                      {Shape: "database", Background: "#9fc5e8", Border: "#0ea5e9"},
	"AWS::Lambda::Function":                     {Shape: "hexagon", Background: "#d5a6bd", Border: "#ec4899"},
}

func visStyleFor(resourceType string) htmlStyle {
	if s, ok := visStyles[resourceType]; ok {
		return s
	}
	return htmlStyle{Shape: "box", Background: "#eeeeee", Border: "#888888"}
}

func isPlaceholderHTML(n model.Node) bool {
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
