package hook

import "goodkind.io/clyde/internal/slogger"

var (
	hookResolveLog = slogger.Concern(slogger.ConcernSessionDomainResolve)
	hookLog        = hookResolveLog.Logger()
)
