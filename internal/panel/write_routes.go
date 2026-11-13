package panel

// writeRoutes registers the API that changes policies, personas and member
// memory.
func (s *Server) writeRoutes() {
	s.api("PUT /panel/api/conversations/{key}/policy", s.putPolicy)
	s.api("POST /panel/api/conversations/{key}/policy/restore", s.restorePolicy)
	s.api("POST /panel/api/conversations/{key}/pause", s.pausePolicy)
	s.api("POST /panel/api/conversations/{key}/resume", s.resumePolicy)
	s.api("PUT /panel/api/conversations/{key}/persona", s.putPersona)
	s.api("PUT /panel/api/conversations/{key}/memory/members/{user_id}", s.putMemberMemory)
	s.api("DELETE /panel/api/conversations/{key}/memory/members/{user_id}", s.deleteMemberMemory)
	s.api("POST /panel/api/conversations/{key}/memory/consolidate", s.consolidateMemory)
}
