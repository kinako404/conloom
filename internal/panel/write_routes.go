package panel

// writeRoutes registers the API that creates, changes and deletes panel bots,
// changes policies, personas, member memory and the hidden state of private
// chats, and the panel chat's writes.
func (s *Server) writeRoutes() {
	s.api("POST /panel/api/bots", s.createBot)
	s.api("PUT /panel/api/bots/{id}", s.updateBot)
	s.api("DELETE /panel/api/bots/{id}", s.deleteBot)
	s.api("PUT /panel/api/conversations/{key}/policy", s.putPolicy)
	s.api("POST /panel/api/conversations/{key}/policy/restore", s.restorePolicy)
	s.api("POST /panel/api/conversations/{key}/pause", s.pausePolicy)
	s.api("POST /panel/api/conversations/{key}/resume", s.resumePolicy)
	s.api("PUT /panel/api/conversations/{key}/persona", s.putPersona)
	s.api("PUT /panel/api/conversations/{key}/memory/members/{user_id}", s.putMemberMemory)
	s.api("DELETE /panel/api/conversations/{key}/memory/members/{user_id}", s.deleteMemberMemory)
	s.api("POST /panel/api/conversations/{key}/memory/consolidate", s.consolidateMemory)
	s.api("PUT /panel/api/conversations/{key}/autonomy/buffs/{name}", s.putAutonomyBar)
	s.api("DELETE /panel/api/conversations/{key}/autonomy/status", s.deleteAutonomyStatus)
	s.api("POST /panel/api/conversations/{key}/messages", s.postChatMessage)
	s.api("POST /panel/api/chats", s.createChat)
	s.api("DELETE /panel/api/conversations/{key}", s.deleteChat)
}
