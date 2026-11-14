package telegram

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

// refusingTransport fails the first refuse sendMessage requests before
// anything is written, like a failed dial, and passes everything else on.
type refusingTransport struct {
	mu      sync.Mutex
	refuse  int
	refused []time.Time
}

func (t *refusingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/sendMessage") {
		t.mu.Lock()
		if t.refuse > 0 {
			t.refuse--
			t.refused = append(t.refused, time.Now())
			t.mu.Unlock()
			return nil, errors.New("dial tcp: connection refused")
		}
		t.mu.Unlock()
	}
	return http.DefaultTransport.RoundTrip(r)
}

func (t *refusingTransport) Refused() []time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]time.Time(nil), t.refused...)
}

func TestDeliver_LaterPartFailingStillAcksTheFirst(t *testing.T) {
	for _, tc := range []struct {
		name    string
		respond func() (int, string)
	}{
		{"refused", func() (int, string) {
			return http.StatusForbidden, tgError(403, "Forbidden: bot was blocked by the user", 0)
		}},
		{"unknown", func() (int, string) { return http.StatusBadGateway, "<html>502</html>" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tg, core := newFakeTG(t), newFakeCore(t)
			tg.SetSend(func(call tgCall, n int) (int, string) {
				if n == 2 {
					return tc.respond()
				}
				return 0, ""
			})
			long := strings.Repeat("好", 3000) + "\n\n" + strings.Repeat("长", 3000) + "\n\n" + strings.Repeat("句", 3000)
			core.Enqueue(replyAction("act_long", long, ""))
			stateDir := t.TempDir()
			startAdapter(t, tgConfig(tg.URL, core.URL, stateDir), nil)

			waitFor(t, 5*time.Second, "the ACK", func() bool { return len(core.Acks()) == 1 })
			ack := core.Acks()[0]
			if ack.Req.Status != conloomapi.AckSent || ack.Req.PlatformMessageID != "20001_501" {
				t.Errorf("ack = %+v, want sent with the first part's ID 20001_501", ack)
			}
			time.Sleep(400 * time.Millisecond)
			if n := len(tg.Calls("sendMessage")); n != 2 {
				t.Errorf("sendMessage calls = %d, want 2: the first part, then the failing second, and no third part or resend", n)
			}
			if n := len(core.Acks()); n != 1 {
				t.Errorf("acks = %d, want 1", n)
			}
			record, err := os.ReadFile(filepath.Join(stateDir, "telegram-dedup.log"))
			if err != nil || !strings.Contains(string(record), "act_long\t") || !strings.Contains(string(record), "\t20001_501\n") {
				t.Errorf("dedup log = %q (err %v), want act_long recorded with 20001_501", record, err)
			}
		})
	}
}

func TestDeliver_NotSentIsLeftForRedeliveryAndSentOnce(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	core.Enqueue(replyAction("act_1", replyText, ""))
	transport := &refusingTransport{refuse: 1}
	startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), func(a *Adapter) {
		a.api.hc = &http.Client{Transport: transport}
	})

	waitFor(t, 5*time.Second, "the ACK after redelivery", func() bool { return len(core.Acks()) == 1 })
	refused := transport.Refused()
	if len(refused) != 1 {
		t.Fatalf("refused sends = %d, want 1", len(refused))
	}
	calls := tg.Calls("sendMessage")
	if len(calls) != 1 {
		t.Fatalf("sendMessage calls reaching Telegram = %d, want 1", len(calls))
	}
	if gap := calls[0].At.Sub(refused[0]); gap < 250*time.Millisecond {
		t.Errorf("resent %v after the failure, want it only after the 300ms lease expired", gap)
	}
	if ack := core.Acks()[0]; ack.ActionID != "act_1" || ack.Req.Status != conloomapi.AckSent || ack.Req.PlatformMessageID != "20001_501" {
		t.Errorf("ack = %+v, want one sent ACK with 20001_501", ack)
	}
	time.Sleep(400 * time.Millisecond)
	if n := len(tg.Calls("sendMessage")); n != 1 {
		t.Errorf("sendMessage calls = %d, want the message sent once", n)
	}
	if n := len(core.Acks()); n != 1 {
		t.Errorf("acks = %d, want 1 (none for the attempt that was not sent)", n)
	}
}

// replyActionTo is replyAction for the private chat of user id.
func replyActionTo(actionID, text, id string) conloomapi.Action {
	a := replyAction(actionID, text, "")
	a.ConversationID, a.PlatformConversationID = "private_"+id, id
	return a
}

// stallingTransport holds every sendChatAction request until release is
// closed and passes everything else on.
type stallingTransport struct {
	release chan struct{}
}

func (t *stallingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/sendChatAction") {
		select {
		case <-t.release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	return http.DefaultTransport.RoundTrip(r)
}

// partAction is replyAction numbered as part index of count.
func partAction(actionID, text string, index, count int) conloomapi.Action {
	a := replyAction(actionID, text, "")
	a.PartIndex, a.PartCount = index, count
	return a
}

func TestDeliver_TypingAfterANonFinalPart(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), nil)

	core.Enqueue(partAction("act_p1", "第一句", 1, 3))
	waitFor(t, 5*time.Second, "the first part's ACK", func() bool { return len(core.Acks()) == 1 })
	waitFor(t, 5*time.Second, "typing after the first part", func() bool { return len(typingCalls(tg)) == 1 })
	if typed, sent := typingCalls(tg)[0].At, tg.Calls("sendMessage")[0].At; typed.Before(sent) {
		t.Errorf("typing sent at %v, before the first part at %v", typed, sent)
	}

	core.Enqueue(partAction("act_p3", "第三句", 3, 3))
	waitFor(t, 5*time.Second, "the last part's ACK", func() bool { return len(core.Acks()) == 2 })
	time.Sleep(300 * time.Millisecond)
	if n := len(typingCalls(tg)); n != 1 {
		t.Errorf("typing calls = %d, want only the one after part 1/3 and none after part 3/3", n)
	}
}

func TestDeliver_NoTypingWithoutPartNumbers(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), nil)

	core.Enqueue(replyAction("act_1", replyText, ""))
	waitFor(t, 5*time.Second, "the ACK", func() bool { return len(core.Acks()) == 1 })
	time.Sleep(300 * time.Millisecond)
	if n := len(tg.Calls("sendChatAction")); n != 0 {
		t.Errorf("sendChatAction calls = %d for an action without part numbers, want 0", n)
	}
	if ack := core.Acks()[0]; ack.Req.Status != conloomapi.AckSent || ack.Req.PlatformMessageID != "20001_501" {
		t.Errorf("ack = %+v, want sent with 20001_501", ack)
	}
}

func TestDeliver_TypingBetweenPartsDoesNotHoldDelivery(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	core.Enqueue(partAction("act_p1", "第一句", 1, 2))
	core.Enqueue(replyActionTo("act_b", "给小红的", "20002"))
	transport := &stallingTransport{release: make(chan struct{})}
	startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), func(a *Adapter) {
		a.api.hc = &http.Client{Transport: transport}
	})
	defer close(transport.release)

	waitFor(t, 2*time.Second, "both ACKs while the typing request hangs", func() bool { return len(core.Acks()) == 2 })
	if n := len(tg.Calls("sendChatAction")); n != 0 {
		t.Errorf("sendChatAction calls reaching Telegram = %d, want the request still held", n)
	}
}

func TestDeliver_NotSentHoldsOnlyItsConversation(t *testing.T) {
	tg, core := newFakeTG(t), newFakeCore(t)
	core.Enqueue(replyActionTo("act_a", "给小明的第一句", "20001"))
	core.Enqueue(replyActionTo("act_b", "给小红的", "20002"))
	core.Enqueue(replyActionTo("act_a2", "给小明的第二句", "20001"))
	transport := &refusingTransport{refuse: 1}
	startAdapter(t, tgConfig(tg.URL, core.URL, t.TempDir()), func(a *Adapter) {
		a.api.hc = &http.Client{Transport: transport}
	})

	waitFor(t, 5*time.Second, "all three ACKs", func() bool { return len(core.Acks()) == 3 })
	refused := transport.Refused()
	if len(refused) != 1 {
		t.Fatalf("refused sends = %d, want 1", len(refused))
	}
	var texts []string
	var other time.Time
	for _, c := range tg.Calls("sendMessage") {
		switch c.num("chat_id") {
		case userXM:
			texts = append(texts, c.text())
		case 20002:
			other = c.At
		}
	}
	if other.IsZero() || other.Sub(refused[0]) > 200*time.Millisecond {
		t.Errorf("the other conversation's reply went out %v after the failure, want it in the same batch", other.Sub(refused[0]))
	}
	if want := []string{"给小明的第一句", "给小明的第二句"}; !slices.Equal(texts, want) {
		t.Errorf("replies to 小明 = %q, want %q in order, each once", texts, want)
	}
}
