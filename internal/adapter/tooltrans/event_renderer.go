package tooltrans

import adapterrender "goodkind.io/clyde/internal/adapter/render"

type EventKind = adapterrender.EventKind
type EventPlanStep = adapterrender.EventPlanStep
type Event = adapterrender.Event
type RendererState = adapterrender.RendererState
type EventRenderer = adapterrender.EventRenderer

const (
	EventAssistantTextDelta  = adapterrender.EventAssistantTextDelta
	EventReasoningSignaled   = adapterrender.EventReasoningSignaled
	EventReasoningDelta      = adapterrender.EventReasoningDelta
	EventReasoningFinished   = adapterrender.EventReasoningFinished
	EventPlanUpdated         = adapterrender.EventPlanUpdated
	EventToolStarted         = adapterrender.EventToolStarted
	EventToolProgress        = adapterrender.EventToolProgress
	EventToolCompleted       = adapterrender.EventToolCompleted
	EventFileChangeStarted   = adapterrender.EventFileChangeStarted
	EventFileChangeProgress  = adapterrender.EventFileChangeProgress
	EventFileChangeCompleted = adapterrender.EventFileChangeCompleted
	EventNotice              = adapterrender.EventNotice
	EventToolCallDelta       = adapterrender.EventToolCallDelta
)

var (
	NewEventRenderer   = adapterrender.NewEventRenderer
	FormatActivityDelta = adapterrender.FormatActivityDelta
)
