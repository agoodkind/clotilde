package adapter

import adaptermodel "goodkind.io/clyde/internal/adapter/model"

const (
	BackendClaude    = adaptermodel.BackendClaude
	BackendShunt     = adaptermodel.BackendShunt
	BackendAnthropic = adaptermodel.BackendAnthropic
	BackendCodex     = adaptermodel.BackendCodex

	EffortLow    = adaptermodel.EffortLow
	EffortMedium = adaptermodel.EffortMedium
	EffortHigh   = adaptermodel.EffortHigh
	EffortXHigh  = adaptermodel.EffortXHigh
	EffortMax    = adaptermodel.EffortMax

	ThinkingDefault  = adaptermodel.ThinkingDefault
	ThinkingAdaptive = adaptermodel.ThinkingAdaptive
	ThinkingEnabled  = adaptermodel.ThinkingEnabled
	ThinkingDisabled = adaptermodel.ThinkingDisabled
)

type (
	ResolvedModel = adaptermodel.ResolvedModel
	Registry      = adaptermodel.Registry
)

var (
	NewRegistry       = adaptermodel.NewRegistry
	ClaudeEffortFlag  = adaptermodel.ClaudeEffortFlag
	ResolveFromConfig = adaptermodel.ResolveFromConfig
)
