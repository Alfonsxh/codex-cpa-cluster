package usage

const ModelMultiplierConfigBase = "user_quota.model_multiplier."

type ModelMultiplierDefinition struct {
	Model   string
	Default float64
}

var modelMultiplierDefinitions = []ModelMultiplierDefinition{
	{Model: "codex-auto-review", Default: 1},
	{Model: "gpt-5.3-codex-spark", Default: 1},
	{Model: "gpt-5.4", Default: 1},
	{Model: "gpt-5.4-mini", Default: 1},
	{Model: "gpt-5.5", Default: 1},
	{Model: "gpt-5.6-luna", Default: 1},
	{Model: "gpt-5.6-sol", Default: 1},
	{Model: "gpt-5.6-terra", Default: 1},
	{Model: "gpt-6-astra", Default: 4},
	{Model: "gpt-image-1.5", Default: 1},
	{Model: "gpt-image-2", Default: 1},
	{Model: "unknown", Default: 1},
}

type WeightPolicy struct {
	ModelMultipliers     map[string]float64
	ReasoningMultipliers map[string]float64
}

func ModelMultiplierDefinitions() []ModelMultiplierDefinition {
	result := make([]ModelMultiplierDefinition, len(modelMultiplierDefinitions))
	copy(result, modelMultiplierDefinitions)
	return result
}

func ModelMultiplierSettingKey(model string) string {
	return ModelMultiplierConfigBase + model
}
