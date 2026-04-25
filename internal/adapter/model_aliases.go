package adapter

import adaptermodel "goodkind.io/clyde/internal/adapter/model"

const (
	BackendClaude    = adaptermodel.BackendClaude
	BackendShunt     = adaptermodel.BackendShunt
	BackendAnthropic = adaptermodel.BackendAnthropic
	BackendFallback  = adaptermodel.BackendFallback
	BackendCodex     = adaptermodel.BackendCodex

	FallbackTriggerExplicit       = adaptermodel.FallbackTriggerExplicit
	FallbackTriggerOnOAuthFailure = adaptermodel.FallbackTriggerOnOAuthFailure
	FallbackTriggerBoth           = adaptermodel.FallbackTriggerBoth

	FallbackEscalationFallbackError = adaptermodel.FallbackEscalationFallbackError
	FallbackEscalationOAuthError    = adaptermodel.FallbackEscalationOAuthError

	EffortLow    = adaptermodel.EffortLow
	EffortMedium = adaptermodel.EffortMedium
	EffortHigh   = adaptermodel.EffortHigh
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

var NewRegistry = adaptermodel.NewRegistry
var ClaudeEffortFlag = adaptermodel.ClaudeEffortFlag
var ResolveFromConfig = adaptermodel.ResolveFromConfig
