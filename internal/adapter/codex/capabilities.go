package codex

import (
	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

const (
	defaultEffectiveContextPercent = 90
)

type CapabilityMode struct {
	WebsocketEnabled bool
}

type CapabilityReport struct {
	AdvertisedContextWindow int
	ObservedContextWindow   int
	EffectiveSafeWindow     int
}

func CapabilityReportForModel(model adaptermodel.ResolvedModel, mode CapabilityMode) CapabilityReport {
	advertised := model.Context
	observed := advertised
	if model.Backend == adaptermodel.BackendCodex && !mode.WebsocketEnabled && model.ObservedContext > 0 {
		observed = model.ObservedContext
	}
	if observed <= 0 {
		observed = advertised
	}
	effective := observed
	if effective > 0 {
		effective = (effective * defaultEffectiveContextPercent) / 100
	}
	return CapabilityReport{
		AdvertisedContextWindow: advertised,
		ObservedContextWindow:   observed,
		EffectiveSafeWindow:     effective,
	}
}

func ApplyCapabilityReport(entry adapteropenai.ModelEntry, report CapabilityReport) adapteropenai.ModelEntry {
	entry.Context = report.ObservedContextWindow
	entry.ContextWindow = report.ObservedContextWindow
	entry.ContextLength = report.ObservedContextWindow
	entry.MaxContextLength = report.ObservedContextWindow
	entry.MaxContextTokens = report.ObservedContextWindow
	entry.MaxModelLen = report.ObservedContextWindow
	entry.MaxTokens = report.ObservedContextWindow
	entry.InputTokenLimit = report.ObservedContextWindow
	entry.MaxInputTokens = report.ObservedContextWindow
	entry.ContextTokenLimit = report.EffectiveSafeWindow
	entry.ContextTokenLimitCamel = report.EffectiveSafeWindow
	entry.ContextTokenLimitForMaxMode = report.EffectiveSafeWindow
	entry.ContextTokenLimitForMaxModeCamel = report.EffectiveSafeWindow
	return entry
}
