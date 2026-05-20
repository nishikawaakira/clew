package graph

import (
	"encoding/json"
	"strings"

	"aws-config-graph/internal/model"
)

// ConfigEdge is an edge candidate inferred from a configurationItem.configuration body.
// account and region are inherited from the source item.
type ConfigEdge struct {
	TargetType       string
	TargetID         string
	RelationshipName string
}

// Standard relationship names used for configuration-derived edges. Kept distinct
// from AWS Config relationship names so the two origins don't accidentally collapse.
const (
	relInVPC          = "In VPC"
	relInSubnet       = "In subnet"
	relUsesSG         = "Uses security group"
	relHasENI         = "Has network interface"
	relRoutesGateway  = "Routes via gateway"
	relRoutesNAT      = "Routes via NAT gateway"
	relRoutesENI      = "Routes via ENI"
	relReferencesSG   = "References security group"
)

// ConfigEdges returns extra edges inferred by reading configurationItem.configuration.
// Only the resource types listed in the PoC spec are handled; everything else returns nil.
func ConfigEdges(item model.ConfigItem) []ConfigEdge {
	if len(item.Configuration) == 0 {
		return nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(item.Configuration, &cfg); err != nil {
		return nil
	}
	switch item.ResourceType {
	case "AWS::EC2::RouteTable":
		return routeTableEdges(cfg)
	case "AWS::EC2::Subnet":
		return subnetEdges(cfg)
	case "AWS::EC2::Instance":
		return instanceEdges(cfg)
	case "AWS::EC2::NetworkInterface":
		return eniEdges(cfg)
	case "AWS::EC2::SecurityGroup":
		return sgEdges(cfg)
	case "AWS::ElasticLoadBalancingV2::LoadBalancer":
		return lbEdges(cfg)
	case "AWS::RDS::DBInstance":
		return rdsEdges(cfg)
	case "AWS::Lambda::Function":
		return lambdaEdges(cfg)
	}
	return nil
}

func routeTableEdges(cfg map[string]any) []ConfigEdge {
	var out []ConfigEdge
	if vpcID := getString(cfg, "vpcId", "VpcId"); vpcID != "" {
		out = append(out, ConfigEdge{TargetType: "AWS::EC2::VPC", TargetID: vpcID, RelationshipName: relInVPC})
	}
	for _, r := range getSlice(cfg, "routes", "Routes") {
		route, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if id := getString(route, "gatewayId", "GatewayId"); id != "" && id != "local" {
			if rt := inferTypeFromID(id); rt != "" {
				out = append(out, ConfigEdge{TargetType: rt, TargetID: id, RelationshipName: relRoutesGateway})
			}
		}
		if id := getString(route, "natGatewayId", "NatGatewayId"); id != "" {
			out = append(out, ConfigEdge{TargetType: "AWS::EC2::NatGateway", TargetID: id, RelationshipName: relRoutesNAT})
		}
		if id := getString(route, "networkInterfaceId", "NetworkInterfaceId"); id != "" {
			out = append(out, ConfigEdge{TargetType: "AWS::EC2::NetworkInterface", TargetID: id, RelationshipName: relRoutesENI})
		}
	}
	return out
}

func subnetEdges(cfg map[string]any) []ConfigEdge {
	var out []ConfigEdge
	if vpcID := getString(cfg, "vpcId", "VpcId"); vpcID != "" {
		out = append(out, ConfigEdge{TargetType: "AWS::EC2::VPC", TargetID: vpcID, RelationshipName: relInVPC})
	}
	return out
}

func instanceEdges(cfg map[string]any) []ConfigEdge {
	var out []ConfigEdge
	if id := getString(cfg, "vpcId", "VpcId"); id != "" {
		out = append(out, ConfigEdge{TargetType: "AWS::EC2::VPC", TargetID: id, RelationshipName: relInVPC})
	}
	if id := getString(cfg, "subnetId", "SubnetId"); id != "" {
		out = append(out, ConfigEdge{TargetType: "AWS::EC2::Subnet", TargetID: id, RelationshipName: relInSubnet})
	}
	// EC2 puts SGs under both `securityGroups` (DescribeInstances) and `groups` (in some payloads).
	for _, key := range []string{"securityGroups", "SecurityGroups", "groups", "Groups"} {
		for _, sg := range getSlice(cfg, key) {
			sgm, ok := sg.(map[string]any)
			if !ok {
				continue
			}
			if id := getString(sgm, "groupId", "GroupId"); id != "" {
				out = append(out, ConfigEdge{TargetType: "AWS::EC2::SecurityGroup", TargetID: id, RelationshipName: relUsesSG})
			}
		}
	}
	for _, ni := range getSlice(cfg, "networkInterfaces", "NetworkInterfaces") {
		nim, ok := ni.(map[string]any)
		if !ok {
			continue
		}
		if id := getString(nim, "networkInterfaceId", "NetworkInterfaceId"); id != "" {
			out = append(out, ConfigEdge{TargetType: "AWS::EC2::NetworkInterface", TargetID: id, RelationshipName: relHasENI})
		}
	}
	return out
}

func eniEdges(cfg map[string]any) []ConfigEdge {
	var out []ConfigEdge
	if id := getString(cfg, "vpcId", "VpcId"); id != "" {
		out = append(out, ConfigEdge{TargetType: "AWS::EC2::VPC", TargetID: id, RelationshipName: relInVPC})
	}
	if id := getString(cfg, "subnetId", "SubnetId"); id != "" {
		out = append(out, ConfigEdge{TargetType: "AWS::EC2::Subnet", TargetID: id, RelationshipName: relInSubnet})
	}
	for _, sg := range getSlice(cfg, "groups", "Groups") {
		sgm, ok := sg.(map[string]any)
		if !ok {
			continue
		}
		if id := getString(sgm, "groupId", "GroupId"); id != "" {
			out = append(out, ConfigEdge{TargetType: "AWS::EC2::SecurityGroup", TargetID: id, RelationshipName: relUsesSG})
		}
	}
	return out
}

func sgEdges(cfg map[string]any) []ConfigEdge {
	var out []ConfigEdge
	if id := getString(cfg, "vpcId", "VpcId"); id != "" {
		out = append(out, ConfigEdge{TargetType: "AWS::EC2::VPC", TargetID: id, RelationshipName: relInVPC})
	}
	for _, key := range []string{"ipPermissions", "IpPermissions", "ipPermissionsEgress", "IpPermissionsEgress"} {
		for _, p := range getSlice(cfg, key) {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			for _, u := range getSlice(pm, "userIdGroupPairs", "UserIdGroupPairs") {
				um, ok := u.(map[string]any)
				if !ok {
					continue
				}
				if id := getString(um, "groupId", "GroupId"); id != "" {
					out = append(out, ConfigEdge{TargetType: "AWS::EC2::SecurityGroup", TargetID: id, RelationshipName: relReferencesSG})
				}
			}
		}
	}
	return out
}

func lbEdges(cfg map[string]any) []ConfigEdge {
	var out []ConfigEdge
	if id := getString(cfg, "vpcId", "VpcId"); id != "" {
		out = append(out, ConfigEdge{TargetType: "AWS::EC2::VPC", TargetID: id, RelationshipName: relInVPC})
	}
	for _, az := range getSlice(cfg, "availabilityZones", "AvailabilityZones") {
		azm, ok := az.(map[string]any)
		if !ok {
			continue
		}
		if id := getString(azm, "subnetId", "SubnetId"); id != "" {
			out = append(out, ConfigEdge{TargetType: "AWS::EC2::Subnet", TargetID: id, RelationshipName: relInSubnet})
		}
	}
	// LB security groups may appear as a flat string array.
	for _, sg := range getSlice(cfg, "securityGroups", "SecurityGroups") {
		switch v := sg.(type) {
		case string:
			if v != "" {
				out = append(out, ConfigEdge{TargetType: "AWS::EC2::SecurityGroup", TargetID: v, RelationshipName: relUsesSG})
			}
		case map[string]any:
			if id := getString(v, "groupId", "GroupId"); id != "" {
				out = append(out, ConfigEdge{TargetType: "AWS::EC2::SecurityGroup", TargetID: id, RelationshipName: relUsesSG})
			}
		}
	}
	return out
}

func rdsEdges(cfg map[string]any) []ConfigEdge {
	var out []ConfigEdge
	group := getMap(cfg, "dBSubnetGroup", "dbSubnetGroup", "DBSubnetGroup")
	if group != nil {
		for _, s := range getSlice(group, "subnets", "Subnets") {
			sm, ok := s.(map[string]any)
			if !ok {
				continue
			}
			if id := getString(sm, "subnetIdentifier", "SubnetIdentifier"); id != "" {
				out = append(out, ConfigEdge{TargetType: "AWS::EC2::Subnet", TargetID: id, RelationshipName: relInSubnet})
			}
		}
		if id := getString(group, "vpcId", "VpcId"); id != "" {
			out = append(out, ConfigEdge{TargetType: "AWS::EC2::VPC", TargetID: id, RelationshipName: relInVPC})
		}
	}
	for _, sg := range getSlice(cfg, "vpcSecurityGroups", "VpcSecurityGroups") {
		sgm, ok := sg.(map[string]any)
		if !ok {
			continue
		}
		if id := getString(sgm, "vpcSecurityGroupId", "VpcSecurityGroupId"); id != "" {
			out = append(out, ConfigEdge{TargetType: "AWS::EC2::SecurityGroup", TargetID: id, RelationshipName: relUsesSG})
		}
	}
	return out
}

func lambdaEdges(cfg map[string]any) []ConfigEdge {
	var out []ConfigEdge
	vpc := getMap(cfg, "vpcConfig", "VpcConfig")
	if vpc == nil {
		return nil
	}
	if id := getString(vpc, "vpcId", "VpcId"); id != "" {
		out = append(out, ConfigEdge{TargetType: "AWS::EC2::VPC", TargetID: id, RelationshipName: relInVPC})
	}
	for _, s := range getSlice(vpc, "subnetIds", "SubnetIds") {
		if str, ok := s.(string); ok && str != "" {
			out = append(out, ConfigEdge{TargetType: "AWS::EC2::Subnet", TargetID: str, RelationshipName: relInSubnet})
		}
	}
	for _, s := range getSlice(vpc, "securityGroupIds", "SecurityGroupIds") {
		if str, ok := s.(string); ok && str != "" {
			out = append(out, ConfigEdge{TargetType: "AWS::EC2::SecurityGroup", TargetID: str, RelationshipName: relUsesSG})
		}
	}
	return out
}

// inferTypeFromID guesses an AWS resource type from common id prefixes.
// Returns "" if the prefix is unknown so the caller can skip the edge.
func inferTypeFromID(id string) string {
	switch {
	case strings.HasPrefix(id, "vpc-"):
		return "AWS::EC2::VPC"
	case strings.HasPrefix(id, "subnet-"):
		return "AWS::EC2::Subnet"
	case strings.HasPrefix(id, "rtb-"):
		return "AWS::EC2::RouteTable"
	case strings.HasPrefix(id, "igw-"):
		return "AWS::EC2::InternetGateway"
	case strings.HasPrefix(id, "nat-"):
		return "AWS::EC2::NatGateway"
	case strings.HasPrefix(id, "sg-"):
		return "AWS::EC2::SecurityGroup"
	case strings.HasPrefix(id, "eni-"):
		return "AWS::EC2::NetworkInterface"
	case strings.HasPrefix(id, "i-"):
		return "AWS::EC2::Instance"
	case strings.HasPrefix(id, "vgw-"):
		return "AWS::EC2::VPNGateway"
	case strings.HasPrefix(id, "tgw-"):
		return "AWS::EC2::TransitGateway"
	case strings.HasPrefix(id, "vpce-"):
		return "AWS::EC2::VPCEndpoint"
	}
	return ""
}

// Generic JSON map helpers tolerant of the camelCase/PascalCase divergence we see
// across AWS Config payloads.

func getString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

func getSlice(m map[string]any, keys ...string) []any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.([]any); ok {
				return s
			}
		}
	}
	return nil
}

func getMap(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if mm, ok := v.(map[string]any); ok {
				return mm
			}
		}
	}
	return nil
}
