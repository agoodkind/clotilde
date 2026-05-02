package cmd

import "goodkind.io/clyde/internal/slogger"

var (
	cmdDispatchLog = slogger.Concern(slogger.ConcernCmdDispatch)
	cmdResumeLog   = slogger.Concern(slogger.ConcernCmdResume)
	cmdUILog       = slogger.Concern(slogger.ConcernUITUILifecycle)
	cmdLog         = cmdDispatchLog.Logger()
)
