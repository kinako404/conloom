package panel

// dataRoutes registers the read API for bots, conversations, messages, the
// panel chat, policies, personas, persona templates and images, memory, the
// hidden state of private chats, tasks and audit events.
func (s *Server) dataRoutes() {
	s.api("GET /panel/api/persona-templates", s.listTemplates)
	s.api("GET /panel/api/persona-templates/{name}", s.getTemplate)
	s.api("GET /panel/api/persona-images/{id}", s.getImage)
	s.api("GET /panel/api/bots", s.listBots)
	s.api("GET /panel/api/bots/skills", s.listBotSkills)
	s.api("GET /panel/api/chats", s.listChats)
	s.api("GET /panel/api/conversations", s.listConversations)
	s.api("GET /panel/api/conversations/{key}", s.getConversation)
	s.api("GET /panel/api/conversations/{key}/messages", s.listMessages)
	s.api("GET /panel/api/conversations/{key}/chat", s.getChat)
	s.api("GET /panel/api/conversations/{key}/policy", s.getPolicy)
	s.api("GET /panel/api/conversations/{key}/persona", s.getPersona)
	s.api("GET /panel/api/conversations/{key}/memory", s.getMemory)
	s.api("GET /panel/api/conversations/{key}/memory/revisions", s.listMemoryRevisions)
	s.api("GET /panel/api/conversations/{key}/autonomy", s.getAutonomy)
	s.api("GET /panel/api/tasks", s.listTasks)
	s.api("GET /panel/api/tasks/{id}", s.getTask)
	s.api("GET /panel/api/audit", s.listAudit)
}
