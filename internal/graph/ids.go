package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// NodeID composes the canonical node_id used across the project.
// Format: account_id:aws_region:resource_type:resource_id
func NodeID(accountID, awsRegion, resourceType, resourceID string) string {
	return fmt.Sprintf("%s:%s:%s:%s", accountID, awsRegion, resourceType, resourceID)
}

// EdgeID derives a deterministic SHA-256 id for a directed labelled edge.
func EdgeID(sourceNodeID, relationshipName, targetNodeID string) string {
	h := sha256.Sum256([]byte(sourceNodeID + "|" + relationshipName + "|" + targetNodeID))
	return hex.EncodeToString(h[:])
}
