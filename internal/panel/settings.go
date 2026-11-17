package panel

import (
	"fmt"
	"net/http"
	"time"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// maxDefaultsVersions is how many versions of a default policy the settings
// page lists.
const maxDefaultsVersions = 50

// defaultsView is a version of a default policy; CreatedAt is null for version
// 0, the built-in default policy before any is stored.
type defaultsView struct {
	Version   int                    `json:"version"`
	Policy    conloomapi.GroupPolicy `json:"policy"`
	Reason    string                 `json:"reason"`
	UpdatedBy string                 `json:"updated_by"`
	CreatedAt *time.Time             `json:"created_at"`
}

func newDefaultsView(d store.PolicyDefaults) defaultsView {
	return defaultsView{Version: d.Version, Policy: d.Policy, Reason: d.Reason, UpdatedBy: d.UpdatedBy, CreatedAt: nullTime(d.CreatedAt)}
}

// defaultsSection is a kind's default policy as the settings page shows it.
type defaultsSection struct {
	Current      defaultsView     `json:"current"`
	Versions     []defaultsView   `json:"versions"`
	Followers    int              `json:"followers"`
	MemoryLimits memoryLimitsView `json:"memory_limits"`
}

// defaultsKind is the request's {kind} path value, writing 404 when it is not
// a kind of default policy.
func (s *Server) defaultsKind(w http.ResponseWriter, r *http.Request) (string, bool) {
	kind := r.PathValue("kind")
	if kind != policy.KindGroup && kind != policy.KindPrivate {
		s.writeError(w, r, http.StatusNotFound, conloomapi.CodeNotFound, fmt.Sprintf("no default policy of kind %q", kind))
		return "", false
	}
	return kind, true
}

// getSettingsPolicies returns, for group and private conversations, the
// current default policy (version 0 with created_at null before one is
// stored), its last 50 versions newest first, how many conversations follow
// it and the memory limits in effect under it.
func (s *Server) getSettingsPolicies(w http.ResponseWriter, r *http.Request, _ *Session) {
	out := map[string]defaultsSection{}
	for _, kind := range []string{policy.KindGroup, policy.KindPrivate} {
		cur := s.d.Policies.Defaults(kind)
		versions, err := s.d.Store.ListPolicyDefaults(r.Context(), kind, maxDefaultsVersions)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		followers, err := s.d.Store.PolicyFollowers(r.Context(), kind)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		views := make([]defaultsView, 0, len(versions))
		for _, d := range versions {
			views = append(views, newDefaultsView(d))
		}
		mp := cur.Policy.Memory
		out[kind] = defaultsSection{Current: newDefaultsView(cur), Versions: views, Followers: len(followers),
			MemoryLimits: memoryLimitsView{
				SummaryMaxChars: memory.SummaryLimit(mp, kind), NoteMaxChars: memory.NoteLimit(mp, kind),
				DefaultSummaryMaxChars: memory.SummaryLimit(conloomapi.MemoryPolicy{}, kind),
				DefaultNoteMaxChars:    memory.NoteLimit(conloomapi.MemoryPolicy{}, kind),
			}}
	}
	s.writeJSON(w, http.StatusOK, out)
}

// putSettingsPolicy stores a new version of the {kind} default policy. A
// field the request leaves out takes its built-in value (policy.Builtin), as
// a field left out of the config file did.
func (s *Server) putSettingsPolicy(w http.ResponseWriter, r *http.Request, sess *Session) {
	kind, ok := s.defaultsKind(w, r)
	if !ok {
		return
	}
	req := struct {
		Policy conloomapi.GroupPolicy `json:"policy"`
		Reason string                 `json:"reason"`
	}{Policy: policy.Builtin(kind)}
	if !s.decode(w, r, &req) {
		return
	}
	s.setDefaults(w, r, sess, kind, req.Policy, req.Reason, "update")
}

// restoreSettingsPolicy stores a stored version of the {kind} default policy
// again as its newest version.
func (s *Server) restoreSettingsPolicy(w http.ResponseWriter, r *http.Request, sess *Session) {
	kind, ok := s.defaultsKind(w, r)
	if !ok {
		return
	}
	var req struct {
		Version int    `json:"version"`
		Reason  string `json:"reason"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if req.Version < 1 {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "version must be a positive integer")
		return
	}
	versions, err := s.d.Store.ListPolicyDefaults(r.Context(), kind, 0)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var old *store.PolicyDefaults
	for i := range versions {
		if versions[i].Version == req.Version {
			old = &versions[i]
			break
		}
	}
	if old == nil {
		s.writeError(w, r, http.StatusNotFound, conloomapi.CodeNotFound, fmt.Sprintf("%s default policy version %d not found", kind, req.Version))
		return
	}
	reason := req.Reason
	if reason == "" {
		reason = fmt.Sprintf("恢复到第 %d 版", req.Version)
	}
	s.setDefaults(w, r, sess, kind, old.Policy, reason, "restore")
}

// setDefaults stores p as the new version of kind's default policy
// (policy.Service.SetDefaults, which audits it) and answers it with the
// warnings it raises (see policyWarnings). An audit failure of a stored
// version is only logged.
func (s *Server) setDefaults(w http.ResponseWriter, r *http.Request, sess *Session, kind string, p conloomapi.GroupPolicy, reason, action string) {
	d, err := s.d.Policies.SetDefaults(r.Context(), kind, p, reason, panelActor(sess), action)
	if !s.versionStored(w, r, d.Version, err) {
		return
	}
	s.writeJSON(w, http.StatusOK, struct {
		Current  defaultsView `json:"current"`
		Warnings []string     `json:"warnings"`
	}{newDefaultsView(d), s.policyWarnings(kind, d.Policy)})
}

// policyWarnings re-checks, after p was saved as kind's default policy, what
// the service warns about at startup: memory settings the long model cannot
// serve (memory.WarnOutputReservation under both default policies and, when p
// is the private one, WarnPrivateMemoryNotSensitive) and a review level of p
// that is not implemented yet. Each warning is logged and returned in
// Chinese; none is an empty list.
func (s *Server) policyWarnings(kind string, p conloomapi.GroupPolicy) []string {
	group, private := s.d.Policies.Defaults(policy.KindGroup).Policy, s.d.Policies.Defaults(policy.KindPrivate).Policy
	if kind == policy.KindPrivate {
		private = p
	} else {
		group = p
	}
	notices := []string{memory.WarnOutputReservation(s.d.Config, group, private, s.log)}
	if kind == policy.KindPrivate {
		notices = append(notices, memory.WarnPrivateMemoryNotSensitive(s.d.Config, private, s.log))
	}
	out := []string{}
	for _, notice := range notices {
		if notice != "" {
			out = append(out, notice)
		}
	}
	if lvl := p.Review.Level; !review.LevelImplemented(lvl) {
		s.log.Warn("model review is not implemented yet; this review level behaves like basic", "kind", kind, "level", lvl)
		out = append(out, fmt.Sprintf("审查级别 %q 还没有实现，目前按 basic 处理", lvl))
	}
	return out
}
