package daemon

import "goodkind.io/clyde/internal/slogger"

var cliDaemonLog = slogger.Concern(slogger.ConcernCmdDispatch)
