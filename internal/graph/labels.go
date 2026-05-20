package graph

import "strings"

// shortLabels maps the verbose AWS::Service::Resource type to a compact display label.
var shortLabels = map[string]string{
	"AWS::EC2::VPC":                             "VPC",
	"AWS::EC2::Subnet":                          "Subnet",
	"AWS::EC2::RouteTable":                      "RouteTable",
	"AWS::EC2::InternetGateway":                 "IGW",
	"AWS::EC2::NatGateway":                      "NATGW",
	"AWS::EC2::SecurityGroup":                   "SecurityGroup",
	"AWS::EC2::NetworkInterface":                "ENI",
	"AWS::EC2::Instance":                        "EC2",
	"AWS::ElasticLoadBalancingV2::LoadBalancer": "LB",
	"AWS::ElasticLoadBalancingV2::TargetGroup":  "TargetGroup",
	"AWS::RDS::DBInstance":                      "RDS",
	"AWS::Lambda::Function":                     "Lambda",
}

// ShortType returns a compact display label for a resource type.
// Unknown types fall back to the last segment after the final "::".
func ShortType(resourceType string) string {
	if s, ok := shortLabels[resourceType]; ok {
		return s
	}
	if idx := strings.LastIndex(resourceType, "::"); idx >= 0 {
		return resourceType[idx+2:]
	}
	return resourceType
}

// MakeLabel returns the display label used for graph_nodes.label.
// Form: "<short-type>\n<resource_id>". The literal `\n` is preserved
// because both DuckDB storage and Mermaid output render it as a newline.
func MakeLabel(resourceType, resourceID string) string {
	return ShortType(resourceType) + "\n" + resourceID
}
