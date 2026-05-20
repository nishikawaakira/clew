package render

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"sort"
	"strings"

	"clew/internal/model"
)

//go:embed html_template.html
var htmlTemplateSrc string

// HTMLOptions controls the HTML render output.
type HTMLOptions struct {
	Title          string
	WithEdgeLabels bool
}

// HTML renders an interactive, self-contained HTML page that draws the graph
// with Cytoscape.js + cytoscape-dagre. Compound nodes are used so that VPCs
// visually contain their subnets and subnets visually contain their instances
// / ENIs, mirroring an AWS architecture diagram. Containment edges are
// suppressed because the nesting communicates the relationship.
//
// The page is fully self-contained but loads cytoscape, dagre and the
// cytoscape-dagre extension from a CDN at view time.
func HTML(w io.Writer, nodes []model.Node, edges []model.Edge, opts HTMLOptions) error {
	if opts.Title == "" {
		opts.Title = "clew — VPC view"
	}

	tmpl, err := template.New("page").Parse(htmlTemplateSrc)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	payload := buildCytoscapePayload(nodes, edges)

	// JSON encode with HTML escaping so user-controlled resource names
	// containing `</script>` or `<` etc. cannot break out of the inline
	// <script> tag.
	var jsonBuf bytes.Buffer
	enc := json.NewEncoder(&jsonBuf)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("marshal cytoscape payload: %w", err)
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

// ---------- Cytoscape payload types ----------

type cyNode struct {
	Data    cyNodeData `json:"data"`
	Classes string     `json:"classes,omitempty"`
}

type cyNodeData struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	Parent       string `json:"parent,omitempty"`
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId"`
	AccountID    string `json:"accountId"`
	AwsRegion    string `json:"awsRegion"`
	ARN          string `json:"arn,omitempty"`
	Placeholder  bool   `json:"placeholder"`
	IsContainer  bool   `json:"isContainer"`
}

type cyEdge struct {
	Data cyEdgeData `json:"data"`
}

type cyEdgeData struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	Label  string `json:"label"`
}

type cyPayload struct {
	Nodes []cyNode `json:"nodes"`
	Edges []cyEdge `json:"edges"`
}

// ---------- Payload construction ----------

func buildCytoscapePayload(nodes []model.Node, edges []model.Edge) cyPayload {
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

	short := make(map[string]string, len(nodes))
	byID := make(map[string]model.Node, len(nodes))
	for i, n := range nodes {
		short[n.NodeID] = fmt.Sprintf("n%d", i+1)
		byID[n.NodeID] = n
	}

	parentByNode := inferParents(edges, byID)

	out := cyPayload{
		Nodes: make([]cyNode, 0, len(nodes)),
		Edges: make([]cyEdge, 0, len(edges)),
	}

	for _, n := range nodes {
		sid := short[n.NodeID]
		cn := cyNode{
			Data: cyNodeData{
				ID:           sid,
				Label:        n.Label,
				ResourceType: n.ResourceType,
				ResourceID:   n.ResourceID,
				AccountID:    n.AccountID,
				AwsRegion:    n.AwsRegion,
				ARN:          n.ARN,
				IsContainer:  isContainerType(n.ResourceType),
			},
			Classes: classFor(n.ResourceType),
		}
		if pid, ok := parentByNode[n.NodeID]; ok {
			if psid, ok := short[pid]; ok {
				cn.Data.Parent = psid
			}
		}
		if isPlaceholderHTML(n) {
			cn.Data.Placeholder = true
			cn.Classes = strings.TrimSpace(cn.Classes + " placeholder")
		}
		out.Nodes = append(out.Nodes, cn)
	}

	seen := make(map[string]struct{}, len(edges))
	for i, e := range edges {
		srcShort, ok1 := short[e.SourceNodeID]
		dstShort, ok2 := short[e.TargetNodeID]
		if !ok1 || !ok2 {
			continue
		}
		// Hide the containment edge that we've already encoded as parent
		// nesting, otherwise we'd draw a redundant arrow on top of the box.
		if pid, ok := parentByNode[e.SourceNodeID]; ok && pid == e.TargetNodeID {
			continue
		}
		key := srcShort + "->" + dstShort + "|" + e.RelationshipName
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out.Edges = append(out.Edges, cyEdge{Data: cyEdgeData{
			ID:     fmt.Sprintf("e%d", i+1),
			Source: srcShort,
			Target: dstShort,
			Label:  e.RelationshipName,
		}})
	}
	return out
}

// inferParents walks the edges and, for every "containment" relationship,
// picks the best parent for each node — preferring Subnet over VPC for things
// that actually live in a subnet (Instance, ENI) and VPC for everything else
// that has a "In VPC" / "Is contained in Vpc" edge.
func inferParents(edges []model.Edge, byID map[string]model.Node) map[string]string {
	candidates := map[string][]string{}
	for _, e := range edges {
		if !isContainmentRelationship(e.RelationshipName) {
			continue
		}
		candidates[e.SourceNodeID] = append(candidates[e.SourceNodeID], e.TargetNodeID)
	}
	parents := make(map[string]string, len(candidates))
	for child, cs := range candidates {
		ch, ok := byID[child]
		if !ok {
			continue
		}
		if best := pickBestParent(ch.ResourceType, cs, byID); best != "" {
			parents[child] = best
		}
	}
	return parents
}

func pickBestParent(childType string, candidates []string, byID map[string]model.Node) string {
	var subnet, vpc string
	for _, pid := range candidates {
		p, ok := byID[pid]
		if !ok {
			continue
		}
		switch p.ResourceType {
		case "AWS::EC2::Subnet":
			if subnet == "" {
				subnet = pid
			}
		case "AWS::EC2::VPC":
			if vpc == "" {
				vpc = pid
			}
		}
	}
	// Children that genuinely live inside a single subnet — prefer subnet.
	switch childType {
	case "AWS::EC2::Instance", "AWS::EC2::NetworkInterface":
		if subnet != "" {
			return subnet
		}
		return vpc
	case "AWS::EC2::Subnet":
		return vpc
	default:
		// RouteTable, SG, LB, RDS, Lambda, IGW, NAT, TargetGroup — these
		// either span multiple subnets or attach at VPC level. The VPC is
		// the right container.
		return vpc
	}
}

// isContainmentRelationship matches the relationship names our graph builder
// emits for "lives in" / "is part of" semantics, both from AWS Config
// relationships and from configuration-derived edges.
func isContainmentRelationship(name string) bool {
	lower := strings.ToLower(name)
	for _, key := range []string{
		"in vpc",
		"in subnet",
		"contained in vpc",
		"contained in subnet",
		"is associated with subnet",
		"is associated with vpc",
	} {
		if strings.Contains(lower, key) {
			return true
		}
	}
	return false
}

func isContainerType(resourceType string) bool {
	switch resourceType {
	case "AWS::EC2::VPC", "AWS::EC2::Subnet":
		return true
	}
	return false
}

// classFor produces a CSS-safe class name from a resource type, e.g.
// AWS::EC2::Instance -> rt-aws-ec2-instance. Used for per-type styling
// in the Cytoscape stylesheet.
func classFor(resourceType string) string {
	r := strings.ReplaceAll(resourceType, "::", "-")
	r = strings.ReplaceAll(r, " ", "-")
	return "rt-" + strings.ToLower(r)
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
