package core

import (
	"context"
	"sync"
	"testing"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/policy"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestOrchestrator_DefaultsChangePausesFollowers(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	env := newTestOrchestrator(t, testOpts{
		short: func(map[string]any) (int, string) { <-release; return 200, fakeReply },
		configure: func(c *config.Config) {
			c.Bots[0].EnabledConversations = append(c.Bots[0].EnabledConversations,
				conloomapi.ConversationRef{Type: conloomapi.ConvGroup, PlatformID: "10002"})
		},
	})
	t.Cleanup(unblock)
	ctx := context.Background()
	ownKey := store.ConversationKey(testBot, "qq", conloomapi.ConvGroup, "group_10002")
	defaults := env.policies.Defaults(policy.KindGroup).Policy
	own := defaults
	own.Concurrency.MaxInteractiveRequests = 1
	if _, err := env.policies.Update(ctx, ownKey, conloomapi.PolicyPut{Policy: own, Reason: "单独设置"}, "admin_29999"); err != nil {
		t.Fatal(err)
	}
	ownEvent := func(user, text string) conloomapi.Event {
		return env.event(conloomapi.ConvGroup, "10002", user, text, mentionBot)
	}

	followRunning := env.submit(t, env.groupEvent("20001", "讲个长故事", mentionBot)).TaskID
	waitUntil(t, "the follower's model call", func() bool { return env.short.Calls() == 1 })
	followQueued := env.submit(t, env.groupEvent("20002", "我也问一个", mentionBot)).TaskID
	ownRunning := env.submit(t, ownEvent("20001", "单独设置的群里问一个")).TaskID
	waitUntil(t, "the own policy's model call", func() bool { return env.short.Calls() == 2 })
	ownQueued := env.submit(t, ownEvent("20002", "单独设置的群里再问一个")).TaskID
	for _, id := range []string{followRunning, followQueued, ownRunning, ownQueued} {
		if id == "" {
			t.Fatalf("tasks = %q, %q, %q, %q; want four", followRunning, followQueued, ownRunning, ownQueued)
		}
	}

	paused := defaults
	paused.Concurrency.MaxInteractiveRequests = 0
	if _, err := env.policies.SetDefaults(ctx, policy.KindGroup, paused, "全部暂停", "panel:admin", "update"); err != nil {
		t.Fatalf("SetDefaults: %v", err)
	}
	env.policies.Wait()
	requireSilentlyCancelled(t, env, followRunning, followQueued)
	if active := env.o.sched.Active(ownKey); len(active) != 2 {
		t.Fatalf("own policy's active tasks after the default change = %+v, want both", active)
	}
	unblock()
	for _, id := range []string{ownRunning, ownQueued} {
		if task := env.waitTask(t, id); task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
			t.Fatalf("own policy's task %s = %+v, want a reply", id, task)
		}
	}

	if _, err := env.policies.SetDefaults(ctx, policy.KindGroup, defaults, "恢复", "panel:admin", "restore"); err != nil {
		t.Fatalf("SetDefaults: %v", err)
	}
	env.policies.Wait()
	resp := env.submit(t, env.groupEvent("20003", "恢复了吗", mentionBot))
	if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == "" {
		t.Fatalf("follower after the defaults resumed: response %+v, want a reply task", resp)
	}
	if task := env.waitTask(t, resp.TaskID); task.Status != conloomapi.TaskSucceeded || len(task.Actions) != 1 {
		t.Fatalf("follower's task after the defaults resumed = %+v, want a reply", task)
	}
}
