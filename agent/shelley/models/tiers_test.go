package models

import "testing"

func TestAssignTiers(t *testing.T) {
	t.Run("Astra and Sol remain peers", func(t *testing.T) {
		tiers := AssignTiers([]string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"})
		for _, id := range []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
			if tiers[id] != Tier1 {
				t.Errorf("%s tier = %d, want %d", id, tiers[id], Tier1)
			}
		}
	})

	t.Run("shadowed model drops to tier 2 when both present", func(t *testing.T) {
		tiers := AssignTiers([]string{"claude-opus-4.8", "claude-opus-4.7"})
		if tiers["claude-opus-4.8"] != Tier1 {
			t.Errorf("opus-4.8 tier = %d, want %d", tiers["claude-opus-4.8"], Tier1)
		}
		if tiers["claude-opus-4.7"] != Tier2 {
			t.Errorf("opus-4.7 tier = %d, want %d", tiers["claude-opus-4.7"], Tier2)
		}
	})

	t.Run("opus 5 shadows opus 4.8", func(t *testing.T) {
		tiers := AssignTiers([]string{"claude-opus-5", "claude-opus-4.8"})
		if tiers["claude-opus-5"] != Tier1 {
			t.Errorf("opus-5 tier = %d, want %d", tiers["claude-opus-5"], Tier1)
		}
		if tiers["claude-opus-4.8"] != Tier2 {
			t.Errorf("opus-4.8 tier = %d, want %d", tiers["claude-opus-4.8"], Tier2)
		}
	})

	t.Run("fable 5.1 shadows fable 5", func(t *testing.T) {
		tiers := AssignTiers([]string{"claude-fable-5.1", "claude-fable-5"})
		if tiers["claude-fable-5.1"] != Tier1 {
			t.Errorf("fable-5.1 tier = %d, want %d", tiers["claude-fable-5.1"], Tier1)
		}
		if tiers["claude-fable-5"] != Tier2 {
			t.Errorf("fable-5 tier = %d, want %d", tiers["claude-fable-5"], Tier2)
		}
	})

	t.Run("worse model stays tier 1 when better absent", func(t *testing.T) {
		tiers := AssignTiers([]string{"claude-opus-4.7"})
		if tiers["claude-opus-4.7"] != Tier1 {
			t.Errorf("opus-4.7 tier = %d, want %d (no shadowing model present)", tiers["claude-opus-4.7"], Tier1)
		}
	})

	t.Run("unknown model defaults to tier 2", func(t *testing.T) {
		tiers := AssignTiers([]string{"some-brand-new-model"})
		if tiers["some-brand-new-model"] != Tier2 {
			t.Errorf("unknown tier = %d, want %d", tiers["some-brand-new-model"], Tier2)
		}
	})

	t.Run("known unshadowed model stays tier 1", func(t *testing.T) {
		tiers := AssignTiers([]string{"gpt-5.6-sol"})
		if tiers["gpt-5.6-sol"] != Tier1 {
			t.Errorf("known tier = %d, want %d", tiers["gpt-5.6-sol"], Tier1)
		}
	})

	t.Run("multiple shadows demote several models", func(t *testing.T) {
		avail := []string{"gpt-5.6-luna", "gpt-5.3-codex", "claude-haiku-4.5"}
		tiers := AssignTiers(avail)
		if tiers["gpt-5.6-luna"] != Tier1 {
			t.Errorf("luna tier = %d, want %d", tiers["gpt-5.6-luna"], Tier1)
		}
		for _, worse := range []string{"gpt-5.3-codex", "claude-haiku-4.5"} {
			if tiers[worse] != Tier2 {
				t.Errorf("%s tier = %d, want %d", worse, tiers[worse], Tier2)
			}
		}
	})

	t.Run("every input id is assigned a tier", func(t *testing.T) {
		avail := IDs()
		tiers := AssignTiers(avail)
		for _, id := range avail {
			if tiers[id] != Tier1 && tiers[id] != Tier2 {
				t.Errorf("%s tier = %d, want 1 or 2", id, tiers[id])
			}
		}
	})
}
