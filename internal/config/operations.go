package config

// Op is a persisted GraphQL operation.
type Op struct {
	OperationName string         `json:"operationName"`
	Extensions    opExtensions   `json:"extensions"`
	Variables     map[string]any `json:"variables,omitempty"`
}

type opExtensions struct {
	PersistedQuery persistedQuery `json:"persistedQuery"`
}

type persistedQuery struct {
	Version    int    `json:"version"`
	Sha256Hash string `json:"sha256Hash"`
}

// RawQuery is a GraphQL request carrying its query text.
type RawQuery struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

func newOp(name, hash string, vars map[string]any) Op {
	return Op{
		OperationName: name,
		Extensions:    opExtensions{PersistedQuery: persistedQuery{Version: 1, Sha256Hash: hash}},
		Variables:     vars,
	}
}

// With returns a copy of the operation with vars deep-merged into its
// default variables.
func (o Op) With(vars map[string]any) Op {
	merged := deepCopy(o.Variables)
	if merged == nil {
		merged = map[string]any{}
	}
	mergeVars(merged, vars)
	o.Variables = merged
	return o
}

func mergeVars(dst, src map[string]any) {
	for k, v := range src {
		if sub, ok := v.(map[string]any); ok {
			if existing, ok := dst[k].(map[string]any); ok {
				mergeVars(existing, sub)
				continue
			}
		}
		dst[k] = v
	}
}

func deepCopy(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			out[k] = deepCopy(sub)
		} else {
			out[k] = v
		}
	}
	return out
}

// GraphQL operations used by the miner. Hashes come from the Twitch web
// client and change occasionally; a PersistedQueryNotFound error means one
// needs updating.
var (
	OpGetStreamInfo = newOp("VideoPlayerStreamInfoOverlayChannel",
		"198492e0857f6aedead9665c81c5a06d67b25b58034649687124083ff288597d",
		map[string]any{"channel": ""})

	OpClaimDrop = newOp("DropsPage_ClaimDropRewards",
		"a455deea71bdc9015b78eb49f4acfbce8baa7ccbedd28e549bb025bd0f751930",
		map[string]any{"input": map[string]any{"dropInstanceID": ""}})

	OpInventory = newOp("Inventory",
		"d86775d0ef16a63a33ad52e80eaff963b2d5b72fada7c991504a57496e1d8e4b",
		map[string]any{"fetchRewardCampaigns": false})

	OpCurrentDrop = newOp("DropCurrentSessionContext",
		"4d06b702d25d652afb9ef835d2a550031f1cf762b193523a92166f40ea3d142b",
		map[string]any{"channelID": "", "channelLogin": ""})

	OpCampaigns = newOp("ViewerDropsDashboard",
		"5a4da2ab3d5b47c9f9ce864e727b2cb346af1e3ea8b897fe8f704a97ff017619",
		map[string]any{"fetchRewardCampaigns": false})

	OpCampaignDetails = newOp("DropCampaignDetails",
		"039277bf98f3130929262cc7c6efd9c141ca3749cb6dca442fc8ead9a53f77c1",
		map[string]any{"channelLogin": "", "dropID": ""})

	OpPlaybackAccessToken = newOp("PlaybackAccessToken",
		"ed230aa1e33e07eebb8928504583da78a5173989fadfb1ac94be06a04f3cdbe9",
		map[string]any{
			"isLive": true, "isVod": false, "login": "",
			"platform": "web", "playerType": "site", "vodID": "",
		})

	OpGameDirectory = newOp("DirectoryPage_Game",
		"cb5dc816e139dcb8a118f14b4b677d59abc224a4b016c4bc2bb00a47fe0ddec4",
		map[string]any{
			"limit":              30,
			"slug":               "",
			"imageWidth":         50,
			"includeCostreaming": false,
			"options": map[string]any{
				"broadcasterLanguages":   []any{},
				"freeformTags":           nil,
				"includeRestricted":      []any{"SUB_ONLY_LIVE"},
				"recommendationsContext": map[string]any{"platform": "web"},
				"sort":                   "RELEVANCE",
				"systemFilters":          []any{},
				"tags":                   []any{},
				"requestID":              "JIRA-VXP-2397",
			},
			"sortTypeIsRecency": false,
		})

	// OpAvailableDrops lists the drop campaigns a channel takes part in. It
	// works for logins whose campaign dashboard Twitch hides.
	OpAvailableDrops = newOp("DropsHighlightService_AvailableDrops",
		"9a62a09bce5b53e26e64a671e530bc599cb6aab1e5ba3cbd5d85966d3940716f",
		map[string]any{"channelID": ""})

	OpNotificationsDelete = newOp("OnsiteNotifications_DeleteNotification",
		"13d463c831f28ffe17dccf55b3148ed8b3edbbd0ebadd56352f1ff0160616816",
		map[string]any{"input": map[string]any{"id": ""}})
)
