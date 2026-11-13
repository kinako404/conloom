package core

import (
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestRemember_RejectsContactsAndIDs(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: privMemConfig(true)})
	for _, text := range []string{
		"/记住 我的手机号是 13800001111",
		"/记住 邮箱 xiaoming@example.com",
		"/记住 身份证 123456789012345678",
		"/记住 QQ 号 20001",
		"/remember 微信 xiaoming_01",
	} {
		if got := env.command(t, "20001", text); got != CopyRememberSensitive {
			t.Errorf("%s = %q, want %q", text, got, CopyRememberSensitive)
		}
	}
	if got := env.privateCommand(t, "20001", "/记住 我的qq是2000123"); got != CopyRememberSensitive {
		t.Errorf("private /记住 with a QQ number = %q", got)
	}
	if m := env.memberIn(t, testConvKey, "20001"); m.Note != "" {
		t.Errorf("group note = %q, want nothing written", m.Note)
	}
	if m := env.memberIn(t, privKeyOf("20001"), "20001"); m.Note != "" {
		t.Errorf("private note = %q, want nothing written", m.Note)
	}
	if got := env.command(t, "20001", "/记住 我 2026 年毕业"); got != CopyRemembered {
		t.Errorf("/记住 with a year = %q, want it remembered", got)
	}
}

func TestRemember_ReviewedLikeInput(t *testing.T) {
	for _, c := range []struct {
		name, action, text, reply, note string
	}{
		{"control characters", "block", "/记住 我下周\u0007三考试", CopyRemembered, memory.RememberMarker + "我下周三考试"},
		{"secret", "block", "/记住 钥匙是 sk-abcdefghijklmnopqrstuv", CopyRemembered, memory.RememberMarker + "钥匙是 [已隐藏]"},
		{"blocked word, block", "block", "/记住 他是个坏词人", CopyRememberBlocked, ""},
		{"blocked word, sanitize", "sanitize", "/记住 他是个坏词人", CopyRemembered, memory.RememberMarker + "他是个**人"},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newTestOrchestrator(t, testOpts{configure: func(cfg *config.Config) {
				privMemConfig(true)(cfg)
				cfg.DefaultGroupPolicy.Review.BlockedWords = []string{"坏词"}
				cfg.DefaultGroupPolicy.Review.Action = c.action
			}})
			if got := env.command(t, "20001", c.text); got != c.reply {
				t.Fatalf("%q = %q, want %q", c.text, got, c.reply)
			}
			if m := env.memberIn(t, testConvKey, "20001"); m.Note != c.note {
				t.Fatalf("note = %q, want %q", m.Note, c.note)
			}
		})
	}
}

func TestMemoryInject_OthersRememberedItemsLeftOut(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{configure: injectConfig(true)})
	env.injectSeed(t, testConvKey, "",
		store.MemberMemory{PlatformUserID: "20001", Note: "喜欢猫。" + memory.RememberMarker + "我下周三考试"},
		store.MemberMemory{PlatformUserID: "20002", Note: "养了狗。" + memory.RememberMarker + "周末要搬家"},
		store.MemberMemory{PlatformUserID: "20003", Note: memory.RememberMarker + "只给自己看的事"})
	red := env.groupEvent("20002", "我家狗会握手了", withName("小红"))
	env.submit(t, red)
	env.submit(t, env.groupEvent("20003", "早"))
	ev := env.groupEvent("20001", "真厉害", withName("小明"), replyTo(red.Message.ID), atUser("user_20003", ""), mentionBot)
	task := env.waitTask(t, env.submit(t, ev).TaskID)
	if task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v", task)
	}
	block, _, _ := strings.Cut(lastContent(env.short.Requests()[0]), "\n\n")
	for _, want := range []string{"\n小明：喜欢猫。（他让我记住）我下周三考试\n", "\n小红：养了狗。\n"} {
		if !strings.Contains(block, want) {
			t.Errorf("block lacks %q:\n%s", want, block)
		}
	}
	for _, banned := range []string{"周末要搬家", "只给自己看的事", "用户20003"} {
		if strings.Contains(block, banned) {
			t.Errorf("block shows another member's remembered item %q:\n%s", banned, block)
		}
	}
}
