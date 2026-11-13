package panel

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/search"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/usage"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// messageView is a stored message; SenderID is the sender's platform user ID.
type messageView struct {
	Seq        int64     `json:"seq"`
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	SenderID   string    `json:"sender_id"`
	SenderName string    `json:"sender_name"`
	FromBot    bool      `json:"from_bot"`
	Text       string    `json:"text"`
	ReplyTo    string    `json:"reply_to"`
}

func newMessageView(m store.MessageRecord) messageView {
	return messageView{
		Seq: m.Seq, ID: m.ID, At: m.OccurredAt.UTC(), SenderID: m.PlatformUserID, SenderName: m.DisplayName,
		FromBot: m.FromBot, Text: m.Text, ReplyTo: m.ReplyTo,
	}
}

// listMessages browses a conversation newest first by seq cursor, or, with a
// non-blank q, searches it as the recall tool does (any term matching, best
// first) and pages by offset.
func (s *Server) listMessages(w http.ResponseWriter, r *http.Request, _ *Session) {
	conv, ok := s.conversation(w, r)
	if !ok {
		return
	}
	limit, err := pageLimit(r)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, err.Error())
		return
	}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		s.searchMessages(w, r, conv, q, limit)
		return
	}
	var before int64
	if v := r.URL.Query().Get("before"); v != "" {
		if before, err = strconv.ParseInt(v, 10, 64); err != nil || before < 1 {
			s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "before must be a positive message seq")
			return
		}
	}
	msgs, err := s.d.Store.MessagesBefore(r.Context(), conv.Key, before, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]messageView, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, newMessageView(m))
	}
	var next *int64
	if len(msgs) == limit {
		next = &msgs[len(msgs)-1].Seq
	}
	s.writeJSON(w, http.StatusOK, struct {
		Messages   []messageView `json:"messages"`
		NextBefore *int64        `json:"next_before"`
	}{out, next})
}

func (s *Server) searchMessages(w http.ResponseWriter, r *http.Request, conv store.ConversationRecord, q string, limit int) {
	query := r.URL.Query()
	offset := 0
	if v := query.Get("offset"); v != "" {
		var err error
		if offset, err = strconv.Atoi(v); err != nil || offset < 0 {
			s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "offset must be a non-negative integer")
			return
		}
	}
	rng, err := searchRange(s.location(), query.Get("from"), query.Get("to"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, err.Error())
		return
	}
	hits, err := s.d.Store.SearchMessages(r.Context(), conv.Key, search.Terms(q, nil), rng, offset, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	hits = hits[:min(len(hits), limit)]
	seqs := make([]int64, 0, len(hits))
	for _, h := range hits {
		seqs = append(seqs, h.Seq)
	}
	found, err := s.d.Store.MessagesBySeq(r.Context(), conv.Key, seqs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]messageView, 0, len(hits))
	for _, seq := range seqs {
		if m, ok := found[seq]; ok {
			out = append(out, newMessageView(m))
		}
	}
	var next *int
	if len(hits) == limit {
		n := offset + len(hits)
		next = &n
	}
	s.writeJSON(w, http.StatusOK, struct {
		Messages   []messageView `json:"messages"`
		NextOffset *int          `json:"next_offset"`
	}{out, next})
}

var errBadSearchDates = errors.New("from and to must be dates as YYYY-MM-DD, from not after to")

// searchRange is the range of the local days from..to in loc, both included;
// an empty date leaves its side open.
func searchRange(loc *time.Location, from, to string) (search.Range, error) {
	var rng search.Range
	var err error
	switch {
	case from != "" && to != "":
		rng.After, rng.Before, err = usage.Range(loc, from, to)
	case from != "":
		rng.After, _, err = usage.Range(loc, from, from)
	case to != "":
		_, rng.Before, err = usage.Range(loc, to, to)
	}
	if err != nil {
		return search.Range{}, errBadSearchDates
	}
	return rng, nil
}
