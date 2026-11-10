//go:build !minimal

package main

func f8Surfaces() []surfaceEntry {
	return []surfaceEntry{
		{Name: "dump", Summary: "Redacted setup summary for support", Phase: phaseF8, Run: runDump},
		{Name: "debug", Summary: "Encrypted local debug share and delete", Phase: phaseF8, Run: runDebug, Usage: debugUsage},
		{Name: "insights", Summary: "Usage analytics from the ledger with a period filter", Phase: phaseF8, Run: runInsights},
		{Name: "journey", Summary: "Chronological skill timeline: list, delete, edit", Phase: phaseF8, Run: runJourney, Usage: journeyUsage},
		{Name: "pets", Summary: "Terminal mascots: install, select, scale, doctor", Phase: phaseF8, Run: runPets, Usage: petsUsage},
	}
}
