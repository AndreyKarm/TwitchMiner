package miner

import "strings"

// IgnoreReason explains why a drop is skipped by the drop-name blacklist.
type IgnoreReason struct {
	// Kind is "keyword" when the name matched, or "precondition" when a
	// required earlier drop is ignored.
	Kind   string
	Detail string
}

// Evaluation is the ignore-policy result for one campaign.
type Evaluation struct {
	Reasons  map[string]*IgnoreReason
	Mineable map[string]bool
}

// evaluateIgnorePolicy marks drops matching a keyword as ignored, along with
// every drop that depends on them, and computes which unclaimed drops still
// lead to a reward worth mining. Prerequisites shared with an allowed
// reward stay mineable.
func evaluateIgnorePolicy(keywords []string, drops []*Drop) *Evaluation {
	byID := make(map[string]*Drop, len(drops))
	for _, d := range drops {
		byID[d.ID] = d
	}
	reasons := map[string]*IgnoreReason{}
	for _, d := range drops {
		if d.IsClaimed {
			continue
		}
		name := strings.ToLower(d.Name)
		for _, k := range keywords {
			if strings.Contains(name, strings.ToLower(k)) {
				reasons[d.ID] = &IgnoreReason{Kind: "keyword", Detail: k}
				break
			}
		}
	}

	// Preconditions are AND-ed: one ignored, unclaimed prerequisite blocks
	// every descendant until it's claimed.
	for changed := true; changed; {
		changed = false
		for _, d := range drops {
			if d.IsClaimed || reasons[d.ID] != nil {
				continue
			}
			for _, id := range d.Preconditions {
				if pre := byID[id]; pre != nil && !pre.IsClaimed && reasons[id] != nil {
					reasons[d.ID] = &IgnoreReason{Kind: "precondition", Detail: pre.Name}
					changed = true
					break
				}
			}
		}
	}

	var branch func(id string, visiting map[string]bool) (map[string]bool, bool)
	branch = func(id string, visiting map[string]bool) (map[string]bool, bool) {
		d := byID[id]
		switch {
		case d == nil:
			return nil, false
		case d.IsClaimed:
			return map[string]bool{}, true
		case reasons[id] != nil || !d.IsWatchDrop() || visiting[id]:
			return nil, false
		}
		visiting[id] = true
		defer delete(visiting, id)
		out := map[string]bool{id: true}
		for _, pre := range d.Preconditions {
			sub, ok := branch(pre, visiting)
			if !ok {
				return nil, false
			}
			for k := range sub {
				out[k] = true
			}
		}
		return out, true
	}

	mineable := map[string]bool{}
	for _, d := range drops {
		if d.IsClaimed || reasons[d.ID] != nil || !d.IsWatchDrop() || len(d.Benefits) == 0 {
			continue
		}
		if ids, ok := branch(d.ID, map[string]bool{}); ok {
			for k := range ids {
				mineable[k] = true
			}
		}
	}
	return &Evaluation{Reasons: reasons, Mineable: mineable}
}
