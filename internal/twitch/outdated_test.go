package twitch

import (
	"slices"
	"testing"
)

func TestOutdatedOperationsAreReportedOnChange(t *testing.T) {
	var g GQL
	var reports [][]string
	g.OnOutdated(func(ops []string) { reports = append(reports, ops) })

	g.setOutdated("Inventory", true)
	g.setOutdated("Inventory", true) // no change, no report
	g.setOutdated("DropCampaignDetails", true)
	g.setOutdated("", true) // raw queries have no name and are never tracked
	g.setOutdated("Inventory", false)
	g.setOutdated("Inventory", false)
	g.setOutdated("DropCampaignDetails", false)

	want := [][]string{
		{"Inventory"},
		{"DropCampaignDetails", "Inventory"},
		{"DropCampaignDetails"},
		{},
	}
	if !slices.EqualFunc(reports, want, slices.Equal) {
		t.Fatalf("reports = %v, want %v", reports, want)
	}
}
