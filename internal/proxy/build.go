package proxy

import (
	"github.com/mariobgsp/routre/internal/config"
	"github.com/mariobgsp/routre/internal/router"
)

// TiersFromConfig converts config tiers into router inputs. It is the
// single home for the config -> router mapping: main's buildRouter and the
// gateway's SIGHUP reload both go through it, so the two can never drift.
func TiersFromConfig(c config.Config) []router.TierInput {
	tiers := make([]router.TierInput, 0, len(c.Tiers))
	for _, t := range c.Tiers {
		provs := make([]router.ProviderInput, 0, len(t.Providers))
		for _, p := range t.Providers {
			provs = append(provs, router.ProviderInput{
				Name:      p.Name,
				Kind:      string(p.Kind),
				BaseURL:   p.BaseURL,
				APIKeyEnv: p.APIKeyEnv,
				Models:    p.Models,
				MaxTokens: p.MaxTokens,
			})
		}
		tiers = append(tiers, router.TierInput{Name: t.Name, Providers: provs})
	}
	return tiers
}

// rtrPolicy extracts the current policy from an existing router (preserved
// across reloads).
func rtrPolicy(rtr *router.Router) router.CooldownPolicy {
	return rtr.Policy()
}
