package session

import "goodkind.io/clyde/internal/slogger"

var (
	sessionResolveLog = slogger.Concern(slogger.ConcernSessionDomainResolve)
	sessionAdoptLog   = slogger.Concern(slogger.ConcernSessionDiscoveryAdopt)
	sessionLog        = sessionResolveLog.Logger()
)
