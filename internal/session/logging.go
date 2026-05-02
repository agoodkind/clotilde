package session

import "goodkind.io/clyde/internal/slogger"

var (
	sessionResolveLog = slogger.Concern(slogger.ConcernSessionDomainResolve)
	sessionScanLog    = slogger.Concern(slogger.ConcernSessionDiscoveryScan)
	sessionAdoptLog   = slogger.Concern(slogger.ConcernSessionDiscoveryAdopt)
	sessionLog        = sessionResolveLog.Logger()
)
