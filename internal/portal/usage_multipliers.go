package portal

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
)

type usageDisplayMultipliers struct {
	Models           map[string]float64 `json:"models"`
	ReasoningEfforts map[string]float64 `json:"reasoning_efforts"`
}

// Display the current Configuration Center policy independently of frozen usage facts.
func (server *Server) currentUsageMultipliers(ctx context.Context) (usageDisplayMultipliers, error) {
	result := usageDisplayMultipliers{
		Models: make(map[string]float64), ReasoningEfforts: make(map[string]float64),
	}
	settings, err := server.identity.ReadSettings(ctx)
	if err != nil {
		return result, err
	}
	read := func(key string, fallback float64) (float64, error) {
		raw, found := settings[key]
		if !found {
			return fallback, nil
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(raw)), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0.1 || value > 10 {
			return 0, fmt.Errorf("invalid configured usage multiplier %s", key)
		}
		return value, nil
	}
	for _, definition := range usage.ModelMultiplierDefinitions() {
		value, err := read(usage.ModelMultiplierSettingKey(definition.Model), definition.Default)
		if err != nil {
			return result, err
		}
		result.Models[definition.Model] = value
	}
	for _, definition := range usage.ReasoningMultiplierDefinitions() {
		value, err := read(usage.ReasoningMultiplierSettingKey(definition.Effort), definition.Default)
		if err != nil {
			return result, err
		}
		result.ReasoningEfforts[definition.Effort] = value
	}
	return result, nil
}
