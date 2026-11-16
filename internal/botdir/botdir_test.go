package botdir

import (
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func ids(bots []config.BotConfig) []string {
	out := make([]string, 0, len(bots))
	for _, b := range bots {
		out = append(out, b.ID)
	}
	return out
}

func TestDirectory_LookupAndOrder(t *testing.T) {
	d := New([]config.BotConfig{
		{ID: "qq-main", Platform: "qq", Name: "小明"},
		{ID: "tg-main", Platform: "telegram", Name: "小红"},
	})
	if got := ids(d.All()); !slices.Equal(got, []string{"qq-main", "tg-main"}) {
		t.Fatalf("All() before SetPanel = %v, want the config bots", got)
	}
	if b, ok := d.Bot("tg-main"); !ok || b.Name != "小红" {
		t.Fatalf("Bot(tg-main) = %+v, %v", b, ok)
	}
	if d.IsPanel("qq-main") || d.Problems("qq-main") != nil {
		t.Fatal("config bot reported as a panel bot or with problems")
	}

	second := PanelBotConfig(store.PanelBot{ID: "panel-b2c3d4e5", Name: "小红", PersonaTemplate: "default"})
	first := PanelBotConfig(store.PanelBot{ID: "panel-a1b2c3d4", Name: "小明", PersonaTemplate: "lighthouse",
		Skills: []string{"memory", "time"}, CreatedBy: "admin", CreatedAt: time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC)})
	problems := []string{"模板已不存在"}
	d.SetPanel([]Entry{
		{Bot: second},
		{Bot: first, Problems: problems},
		{Bot: config.BotConfig{ID: "qq-main", Platform: store.PlatformPanel, Name: "冒名"}},
		{Bot: config.BotConfig{ID: "panel-b2c3d4e5", Platform: store.PlatformPanel, Name: "重复"}},
	})
	if got, want := ids(d.All()), []string{"qq-main", "tg-main", "panel-b2c3d4e5", "panel-a1b2c3d4"}; !slices.Equal(got, want) {
		t.Fatalf("All() = %v, want %v (config order, then snapshot order, taken IDs left out)", got, want)
	}
	if b, ok := d.Bot("qq-main"); !ok || b.Platform != "qq" || b.Name != "小明" || d.IsPanel("qq-main") {
		t.Fatalf("Bot(qq-main) = %+v, %v, IsPanel %v; want the config bot", b, ok, d.IsPanel("qq-main"))
	}
	if b, ok := d.Bot("panel-b2c3d4e5"); !ok || b.Name != "小红" {
		t.Fatalf("Bot(panel-b2c3d4e5) = %+v, %v; want the first entry with that ID", b, ok)
	}
	want := config.BotConfig{ID: "panel-a1b2c3d4", Platform: "panel", Name: "小明", PrivateChat: "admins",
		PrivateTemplate: "lighthouse", GroupTemplate: "lighthouse", Skills: []string{"memory", "time"}}
	if b, ok := d.Bot("panel-a1b2c3d4"); !ok || !reflect.DeepEqual(b, want) {
		t.Fatalf("Bot(panel-a1b2c3d4) = %+v, %v; want %+v", b, ok, want)
	}
	if !d.IsPanel("panel-a1b2c3d4") || !d.IsPanel("panel-b2c3d4e5") {
		t.Fatal("panel bots not reported as panel bots")
	}
	if got := d.Problems("panel-a1b2c3d4"); !slices.Equal(got, problems) {
		t.Fatalf("Problems = %v, want %v", got, problems)
	}
	if got := d.Problems("panel-b2c3d4e5"); got != nil {
		t.Fatalf("Problems of a bot without problems = %v", got)
	}
	if _, ok := d.Bot("panel-zzzzzzzz"); ok || d.IsPanel("panel-zzzzzzzz") || d.Problems("panel-zzzzzzzz") != nil {
		t.Fatal("unknown bot found")
	}

	problems[0] = "改过"
	first.Skills[0] = "改过"
	if b, _ := d.Bot("panel-a1b2c3d4"); b.Skills[0] != "memory" || d.Problems("panel-a1b2c3d4")[0] != "模板已不存在" {
		t.Fatalf("snapshot changed through the entries given to SetPanel: %+v, %v", b, d.Problems("panel-a1b2c3d4"))
	}

	d.SetPanel(nil)
	if got := ids(d.All()); !slices.Equal(got, []string{"qq-main", "tg-main"}) {
		t.Fatalf("All() after SetPanel(nil) = %v", got)
	}
	if _, ok := d.Bot("panel-a1b2c3d4"); ok || d.IsPanel("panel-a1b2c3d4") {
		t.Fatal("removed panel bot still found")
	}
}

func TestDirectory_TemplateUsers(t *testing.T) {
	d := New([]config.BotConfig{
		{ID: "qq-main", Platform: "qq", Name: "小明", PrivateTemplate: "lighthouse", GroupTemplate: "night-owl"},
		{ID: "tg-main", Platform: "telegram", PrivateTemplate: "default", GroupTemplate: "lighthouse"},
		{ID: "qq-alt", Platform: "qq", Name: "小明", PrivateTemplate: "cat", GroupTemplate: "cat"},
	})
	if got := d.TemplateUsers("lighthouse"); !slices.Equal(got, []string{"qq-main", "tg-main"}) {
		t.Fatalf("TemplateUsers without panel bots = %v, want the config bots using it for private chats or groups", got)
	}
	if got := d.TemplateUsers("night-owl"); !slices.Equal(got, []string{"qq-main"}) {
		t.Fatalf("TemplateUsers(night-owl) = %v, want qq-main, whose groups start from it", got)
	}
	if got := d.TemplateUsers("cat"); !slices.Equal(got, []string{"qq-alt"}) {
		t.Fatalf("TemplateUsers(cat) = %v, want qq-alt once", got)
	}
	d.SetPanel([]Entry{
		{Bot: PanelBotConfig(store.PanelBot{ID: "panel-a1b2c3d4", Name: "小红", PersonaTemplate: "lighthouse"})},
		{Bot: PanelBotConfig(store.PanelBot{ID: "panel-b2c3d4e5", Name: "小刚", PersonaTemplate: "default"})},
		{Bot: PanelBotConfig(store.PanelBot{ID: "panel-c3d4e5f6", Name: "小绿", PersonaTemplate: "lighthouse"})},
	})
	if got, want := d.TemplateUsers("lighthouse"), []string{"qq-main", "tg-main", "panel-a1b2c3d4", "panel-c3d4e5f6"}; !slices.Equal(got, want) {
		t.Fatalf("TemplateUsers(lighthouse) = %v, want %v: config bots, then panel bots", got, want)
	}
	if got := d.TemplateUsers("nope"); len(got) != 0 {
		t.Fatalf("TemplateUsers(nope) = %v, want none", got)
	}

	d.SetConfig([]Entry{{Bot: config.BotConfig{ID: "qq-alt", Platform: "qq", Name: "小明", PrivateTemplate: "default",
		GroupTemplate: "default"}, Problems: []string{"模板已不存在"}}})
	if got := d.TemplateUsers("cat"); len(got) != 0 {
		t.Fatalf("TemplateUsers(cat) = %v, want none: qq-alt uses default instead", got)
	}
	if got := d.TemplateUsers("default"); !slices.Equal(got, []string{"tg-main", "qq-alt", "panel-b2c3d4e5"}) {
		t.Fatalf("TemplateUsers(default) = %v, want the templates as they take effect", got)
	}
}

func TestDirectory_SetConfig(t *testing.T) {
	configured := []config.BotConfig{
		{ID: "qq-main", Platform: "qq", Name: "小明", PrivateTemplate: "night-owl", GroupTemplate: "night-owl", Skills: []string{"memory"},
			Admins: []string{"29999"}, EnabledConversations: []conloomapi.ConversationRef{{Type: conloomapi.ConvGroup, PlatformID: "10001"}}},
		{ID: "tg-main", Platform: "telegram", Name: "小红", PrivateTemplate: "lighthouse", GroupTemplate: "lighthouse"},
	}
	d := New(configured)
	if got := d.ConfigBots(); !reflect.DeepEqual(got, configured) {
		t.Fatalf("ConfigBots = %+v, want the bots as configured", got)
	}

	fallback := configured[0]
	fallback.PrivateTemplate, fallback.GroupTemplate = "default", "default"
	fallback.Admins = slices.Clone(fallback.Admins)
	fallback.EnabledConversations = slices.Clone(fallback.EnabledConversations)
	problems := []string{"人设模板“night-owl”已不存在，暂用 default"}
	d.SetConfig([]Entry{
		{Bot: fallback, Problems: problems},
		{Bot: config.BotConfig{ID: "panel-a1b2c3d4", Platform: store.PlatformPanel, Name: "冒名"}},
	})
	if got := ids(d.All()); !slices.Equal(got, []string{"qq-main", "tg-main"}) {
		t.Fatalf("All() = %v, want only the config bots, in config order", got)
	}
	if b, ok := d.Bot("qq-main"); !ok || !reflect.DeepEqual(b, fallback) {
		t.Fatalf("Bot(qq-main) = %+v, %v; want it as it takes effect %+v", b, ok, fallback)
	}
	if got := d.All()[0]; !reflect.DeepEqual(got, fallback) {
		t.Fatalf("All()[0] = %+v, want %+v", got, fallback)
	}
	if got := d.Problems("qq-main"); !slices.Equal(got, problems) {
		t.Fatalf("Problems(qq-main) = %v, want %v", got, problems)
	}
	if b, _ := d.Bot("tg-main"); !reflect.DeepEqual(b, configured[1]) || d.Problems("tg-main") != nil {
		t.Fatalf("Bot(tg-main) = %+v, problems %v; want it as configured without problems", b, d.Problems("tg-main"))
	}
	if d.IsPanel("qq-main") {
		t.Fatal("config bot reported as a panel bot")
	}
	if _, ok := d.Bot("panel-a1b2c3d4"); ok {
		t.Fatal("SetConfig added a bot that is not a config bot")
	}
	if got := d.ConfigBots(); got[0].PrivateTemplate != "night-owl" {
		t.Fatalf("ConfigBots after SetConfig = %+v, want the bots as configured", got)
	}

	problems[0] = "改过"
	fallback.Skills[0], fallback.Admins[0], fallback.EnabledConversations[0].PlatformID = "改过", "改过", "改过"
	if b, _ := d.Bot("qq-main"); b.Skills[0] != "memory" || b.Admins[0] != "29999" || b.EnabledConversations[0].PlatformID != "10001" ||
		d.Problems("qq-main")[0] == "改过" {
		t.Fatalf("directory changed through the entries given to SetConfig: %+v, %v", b, d.Problems("qq-main"))
	}

	d.SetConfig(nil)
	if b, _ := d.Bot("qq-main"); b.PrivateTemplate != "night-owl" || d.Problems("qq-main") != nil {
		t.Fatalf("Bot(qq-main) after SetConfig(nil) = %+v, problems %v; want it as configured", b, d.Problems("qq-main"))
	}
}

func TestPanelBotConfig(t *testing.T) {
	skills := []string{"memory"}
	b := PanelBotConfig(store.PanelBot{ID: "panel-a1b2c3d4", Name: "小明", PersonaTemplate: "default", Skills: skills})
	want := config.BotConfig{ID: "panel-a1b2c3d4", Platform: store.PlatformPanel, Name: "小明", PrivateChat: "admins",
		PrivateTemplate: "default", GroupTemplate: "default", Skills: []string{"memory"}}
	if !reflect.DeepEqual(b, want) {
		t.Fatalf("PanelBotConfig = %+v, want %+v", b, want)
	}
	skills[0] = "time"
	if b.Skills[0] != "memory" {
		t.Fatal("PanelBotConfig shares the stored bot's skills")
	}
}

// TestDirectory_ConcurrentSetAndRead swaps the panel snapshot while readers
// look bots up; run with -race. Every snapshot names all its bots after its
// generation, so a reader sees one whole snapshot or another, never a mix.
func TestDirectory_ConcurrentSetAndRead(t *testing.T) {
	d := New([]config.BotConfig{{ID: "qq-main", Platform: "qq", Name: "小明"}})
	snapshot := func(gen int) []Entry {
		name := fmt.Sprintf("第%d版", gen)
		return []Entry{
			{Bot: PanelBotConfig(store.PanelBot{ID: "panel-a1b2c3d4", Name: name, Skills: []string{"memory"}}), Problems: []string{name}},
			{Bot: PanelBotConfig(store.PanelBot{ID: "panel-b2c3d4e5", Name: name})},
		}
	}
	d.SetPanel(snapshot(0))

	const writes = 500
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		for gen := 1; gen <= writes; gen++ {
			d.SetPanel(snapshot(gen))
		}
	}()
	errs := make(chan string, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				all := d.All()
				if len(all) != 3 || all[0].ID != "qq-main" || all[1].Name != all[2].Name {
					errs <- fmt.Sprintf("All() = %+v, want qq-main then two panel bots of one generation", all)
					return
				}
				if b, ok := d.Bot("panel-a1b2c3d4"); !ok || !d.IsPanel(b.ID) || len(b.Skills) != 1 {
					errs <- fmt.Sprintf("Bot(panel-a1b2c3d4) = %+v, %v", b, ok)
					return
				}
				if p := d.Problems("panel-a1b2c3d4"); len(p) != 1 {
					errs <- fmt.Sprintf("Problems = %v", p)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if b, _ := d.Bot("panel-b2c3d4e5"); b.Name != fmt.Sprintf("第%d版", writes) {
		t.Fatalf("after the writes Bot = %+v, want the last generation", b)
	}
}

func TestConfigBot_SettingsAndDefaults(t *testing.T) {
	identity := config.BotConfig{ID: "qq-main", Platform: "qq", PlatformSelfID: "30001"}
	s := store.BotSettings{BotInstanceID: "qq-main", Name: "小明", Admins: []string{"29999"}, PrivateChat: "allowlist",
		PrivateAllowlist: []string{"20001"}, Groups: []string{"10001", "10002"}, PrivateTemplate: "lighthouse",
		GroupTemplate: "night-owl", Skills: []string{"memory"}, UpdatedBy: "panel:admin"}
	b := ConfigBot(config.BotConfig{ID: "qq-main", Platform: "qq", PlatformSelfID: "30001", Name: "旧名字",
		Legacy: config.LegacyBotSettings{Admins: &[]string{"20002"}}}, s)
	want := config.BotConfig{ID: "qq-main", Platform: "qq", PlatformSelfID: "30001", Name: "小明", Admins: []string{"29999"},
		EnabledConversations: []conloomapi.ConversationRef{{Type: conloomapi.ConvGroup, PlatformID: "10001"},
			{Type: conloomapi.ConvGroup, PlatformID: "10002"}, {Type: conloomapi.ConvPrivate, PlatformID: "20001"}},
		PrivateChat: "allowlist", PrivateTemplate: "lighthouse", GroupTemplate: "night-owl", Skills: []string{"memory"}}
	if !reflect.DeepEqual(b, want) {
		t.Fatalf("ConfigBot = %+v\nwant %+v: the identity of the bot, the rest from the settings", b, want)
	}
	s.Admins[0], s.Skills[0] = "改过", "改过"
	if b.Admins[0] != "29999" || b.Skills[0] != "memory" {
		t.Fatal("ConfigBot shares the settings' lists")
	}
	s.Admins[0], s.Skills[0], s.UpdatedBy = "29999", "memory", ""
	if got := Settings(b); !reflect.DeepEqual(got, s) {
		t.Fatalf("Settings(ConfigBot(s)) = %+v, want %+v", got, s)
	}

	d := DefaultSettings("tg-main")
	wantDefault := store.BotSettings{BotInstanceID: "tg-main", Name: "tg-main", Admins: []string{}, PrivateChat: "admins",
		PrivateAllowlist: []string{}, Groups: []string{}, PrivateTemplate: "default", GroupTemplate: "default", Skills: []string{}}
	if !reflect.DeepEqual(d, wantDefault) {
		t.Fatalf("DefaultSettings = %+v, want %+v", d, wantDefault)
	}
	tg := ConfigBot(config.BotConfig{ID: "tg-main", Platform: "telegram", PlatformSelfID: "30002"}, d)
	if tg.Name != "tg-main" || len(tg.Admins) != 0 || tg.PrivateChat != "admins" || len(tg.EnabledConversations) != 0 ||
		tg.TemplateFor(conloomapi.ConvGroup) != "default" || tg.TemplateFor(conloomapi.ConvPrivate) != "default" || len(tg.Skills) != 0 {
		t.Fatalf("config bot with the default settings = %+v", tg)
	}
	if got := Settings(identity); got.Admins == nil || got.Groups == nil || got.PrivateAllowlist == nil || got.Skills == nil {
		t.Fatalf("Settings of a bot without lists = %+v, want empty lists, not nil", got)
	}
}

func TestLegacySettingsAndWithDefaults(t *testing.T) {
	name, template := "小明", "lighthouse"
	bots := []config.BotConfig{
		{ID: "qq-main", Platform: "qq", PlatformSelfID: "30001", Legacy: config.LegacyBotSettings{Name: &name, PersonaTemplate: &template,
			EnabledConversations: &[]conloomapi.ConversationRef{{Type: conloomapi.ConvGroup, PlatformID: "10001"},
				{Type: conloomapi.ConvPrivate, PlatformID: "20001"}}}},
		{ID: "tg-main", Platform: "telegram", PlatformSelfID: "30002"},
	}
	want := []store.BotSettings{
		{BotInstanceID: "qq-main", Name: "小明", Admins: []string{}, PrivateChat: "admins", PrivateAllowlist: []string{"20001"},
			Groups: []string{"10001"}, PrivateTemplate: "lighthouse", GroupTemplate: "lighthouse", Skills: []string{}, UpdatedBy: "system"},
		{BotInstanceID: "tg-main", Name: "织语", Admins: []string{}, PrivateChat: "admins", PrivateAllowlist: []string{},
			Groups: []string{}, PrivateTemplate: "default", GroupTemplate: "default", Skills: []string{}, UpdatedBy: "system"},
	}
	if got := LegacySettings(bots); !reflect.DeepEqual(got, want) {
		t.Fatalf("LegacySettings = %+v\nwant %+v: the file's keys, the old defaults for the rest", got, want)
	}

	given := WithDefaults(bots)
	if len(given) != 2 {
		t.Fatalf("WithDefaults = %+v", given)
	}
	for i, b := range given {
		if want := ConfigBot(bots[i], DefaultSettings(bots[i].ID)); !reflect.DeepEqual(b, want) {
			t.Errorf("WithDefaults[%d] = %+v, want %+v: the defaults, not the legacy keys", i, b, want)
		}
	}
	if given[0].Name != "qq-main" || given[0].Legacy.Name != nil {
		t.Errorf("WithDefaults[0] = %+v, want named after its ID without the legacy keys", given[0])
	}
}
