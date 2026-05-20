package query

import (
	"context"

	"aws-config-graph/internal/model"
	"aws-config-graph/internal/store"
)

// BFS walks the graph outward from every node whose resource_id matches
// startResourceID. Edges are followed in both directions. depth=0 returns
// only the seed nodes.
func BFS(ctx context.Context, s *store.Store, startResourceID string, depth int) ([]model.Node, []model.Edge, error) {
	seeds, err := s.FindNodesByResourceID(ctx, startResourceID)
	if err != nil {
		return nil, nil, err
	}
	if len(seeds) == 0 {
		return nil, nil, nil
	}

	visited := make(map[string]model.Node, len(seeds))
	edgeSet := map[string]model.Edge{}
	for _, n := range seeds {
		visited[n.NodeID] = n
	}

	frontier := make([]string, 0, len(seeds))
	for _, n := range seeds {
		frontier = append(frontier, n.NodeID)
	}

	for d := 0; d < depth; d++ {
		if len(frontier) == 0 {
			break
		}
		edges, err := s.NeighborEdges(ctx, frontier)
		if err != nil {
			return nil, nil, err
		}
		var next []string
		nextSet := map[string]struct{}{}
		for _, e := range edges {
			if _, ok := edgeSet[e.EdgeID]; ok {
				continue
			}
			edgeSet[e.EdgeID] = e
			for _, candidate := range []string{e.SourceNodeID, e.TargetNodeID} {
				if _, ok := visited[candidate]; ok {
					continue
				}
				if _, ok := nextSet[candidate]; ok {
					continue
				}
				nextSet[candidate] = struct{}{}
				next = append(next, candidate)
			}
		}
		if len(next) > 0 {
			fetched, err := s.GetNodes(ctx, next)
			if err != nil {
				return nil, nil, err
			}
			for _, n := range fetched {
				visited[n.NodeID] = n
			}
		}
		frontier = next
	}

	nodes := make([]model.Node, 0, len(visited))
	for _, n := range visited {
		nodes = append(nodes, n)
	}
	edges := make([]model.Edge, 0, len(edgeSet))
	for _, e := range edgeSet {
		edges = append(edges, e)
	}
	return nodes, edges, nil
}
