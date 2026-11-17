package panel

// writeRoutes registers the API that creates, changes and deletes panel bots
// and persona templates, imports persona templates, uploads and describes
// persona images, changes the default policies, policies, personas, member
// memory and the hidden state of private chats, and the panel chat's writes.
func (s *Server) writeRoutes() {
	s.api("POST /panel/api/persona-templates/import", s.importTemplates)
	s.api("PUT /panel/api/persona-templates/{name}", s.putTemplate)
	s.api("POST /panel/api/persona-templates/{name}/reset", s.resetTemplate)
	s.api("DELETE /panel/api/persona-templates/{name}", s.deleteTemplate)
	s.api("POST /panel/api/persona-images", s.uploadImage)
	s.api("POST /panel/api/persona-images/{id}/describe", s.describeImage)
	s.api("POST /panel/api/bots", s.createBot)
	s.api("PUT /panel/api/bots/{id}", s.updateBot)
	s.api("DELETE /panel/api/bots/{id}", s.deleteBot)
	s.api("PUT /panel/api/settings/policies/{kind}", s.putSettingsPolicy)
	s.api("POST /panel/api/settings/policies/{kind}/restore", s.restoreSettingsPolicy)
	s.api("PUT /panel/api/conversations/{key}/policy", s.putPolicy)
	s.api("POST /panel/api/conversations/{key}/policy/restore", s.restorePolicy)
	s.api("POST /panel/api/conversations/{key}/policy/follow-defaults", s.followDefaults)
	s.api("POST /panel/api/conversations/{key}/pause", s.pausePolicy)
	s.api("POST /panel/api/conversations/{key}/resume", s.resumePolicy)
	s.api("PUT /panel/api/conversations/{key}/persona", s.putPersona)
	s.api("PUT /panel/api/conversations/{key}/memory/members/{user_id}", s.putMemberMemory)
	s.api("DELETE /panel/api/conversations/{key}/memory/members/{user_id}", s.deleteMemberMemory)
	s.api("POST /panel/api/conversations/{key}/memory/consolidate", s.consolidateMemory)
	s.api("PUT /panel/api/conversations/{key}/autonomy/buffs/{name}", s.putAutonomyBar)
	s.api("DELETE /panel/api/conversations/{key}/autonomy/bars/{name}", s.deleteAutonomyBar)
	s.api("DELETE /panel/api/conversations/{key}/autonomy/effects/{name}", s.deleteAutonomyEffect)
	s.api("DELETE /panel/api/conversations/{key}/autonomy/status", s.deleteAutonomyStatus)
	s.api("POST /panel/api/conversations/{key}/messages", s.postChatMessage)
	s.api("DELETE /panel/api/conversations/{key}/messages/{seq}", s.withdrawChatMessage)
	s.api("POST /panel/api/conversations/{key}/messages/{seq}/regenerate", s.regenerateChatMessage)
	s.api("POST /panel/api/conversations/{key}/suggestions", s.postSuggestions)
	s.api("POST /panel/api/chats", s.createChat)
	s.api("DELETE /panel/api/conversations/{key}", s.deleteChat)
}
