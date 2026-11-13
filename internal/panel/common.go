package panel

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

// Store is what the panel API reads and writes besides sessions; *sqlite.DB
// is one.
type Store interface {
	store.Store
	GetConversationByKey(ctx context.Context, key string) (store.ConversationRecord, error)
	ConversationStats(ctx context.Context) ([]store.ConversationStats, error)
	MessagesBefore(ctx context.Context, convKey string, beforeSeq int64, limit int) ([]store.MessageRecord, error)
	MessagesBySeq(ctx context.Context, convKey string, seqs []int64) (map[int64]store.MessageRecord, error)
	ListTasks(ctx context.Context, f store.TaskFilter) ([]store.TaskRecord, error)
	ListAudit(ctx context.Context, f store.AuditFilter) ([]store.AuditEvent, error)
	ListPersonaVersions(ctx context.Context, convKey string) ([]store.PersonaVersion, error)
	CountPendingOutbox(ctx context.Context) (int, error)
	UsageBuckets(ctx context.Context, from, to time.Time) ([]store.UsageBucket, error)
	LatencySamples(ctx context.Context, from, to time.Time) ([]store.LatencySample, error)
	ListModelCalls(ctx context.Context, f store.ModelCallFilter) ([]store.ModelCall, error)
}

const (
	defaultPageLimit = 50
	maxPageLimit     = 200
)

var errBadLimit = errors.New("limit must be a positive integer")

// pageLimit is the request's limit query parameter: 50 when absent, at most
// 200. A value that is not a positive integer is errBadLimit.
func pageLimit(r *http.Request) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return defaultPageLimit, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, errBadLimit
	}
	return min(n, maxPageLimit), nil
}

// conversation resolves the request's {key} path value to a stored
// conversation, writing 404 when there is none.
func (s *Server) conversation(w http.ResponseWriter, r *http.Request) (store.ConversationRecord, bool) {
	conv, err := s.d.Store.GetConversationByKey(r.Context(), r.PathValue("key"))
	if err != nil {
		s.fail(w, r, err)
		return store.ConversationRecord{}, false
	}
	return conv, true
}

// location is the server time zone, UTC when none was given.
func (s *Server) location() *time.Location {
	if s.d.Location == nil {
		return time.UTC
	}
	return s.d.Location
}
