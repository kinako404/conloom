package panel

// dataRoutes registers the read API for conversations, messages, policies,
// personas, memory, tasks and audit events.
func (s *Server) dataRoutes() {
	s.api("GET /panel/api/conversations", s.listConversations)
	s.api("GET /panel/api/conversations/{key}", s.getConversation)
	s.api("GET /panel/api/conversations/{key}/messages", s.listMessages)
	s.api("GET /panel/api/conversations/{key}/policy", s.getPolicy)
	s.api("GET /panel/api/conversations/{key}/persona", s.getPersona)
	s.api("GET /panel/api/conversations/{key}/memory", s.getMemory)
	s.api("GET /panel/api/conversations/{key}/memory/revisions", s.listMemoryRevisions)
	s.api("GET /panel/api/tasks", s.listTasks)
	s.api("GET /panel/api/tasks/{id}", s.getTask)
	s.api("GET /panel/api/audit", s.listAudit)
}
