package panel

// overviewRoutes registers the overview, usage, live metrics and config API.
func (s *Server) overviewRoutes() {
	s.api("GET /panel/api/overview", s.overview)
	s.api("GET /panel/api/usage", s.usageReport)
	s.api("GET /panel/api/usage/calls", s.usageCalls)
	s.api("GET /panel/api/metrics/live", s.metricsLive)
	s.api("GET /panel/api/config", s.configView)
}
