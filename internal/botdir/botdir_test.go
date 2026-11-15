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
	d.SetPanel([]PanelEntry{
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
		PersonaTemplate: "lighthouse", Skills: []string{"memory", "time"}}
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

func TestDirectory_PanelTemplateUsers(t *testing.T) {
	d := New([]config.BotConfig{{ID: "qq-main", Platform: "qq", Name: "小明", PersonaTemplate: "lighthouse"}})
	if got := d.PanelTemplateUsers("lighthouse"); len(got) != 0 {
		t.Fatalf("PanelTemplateUsers without panel bots = %v, want none", got)
	}
	d.SetPanel([]PanelEntry{
		{Bot: PanelBotConfig(store.PanelBot{ID: "panel-a1b2c3d4", Name: "小红", PersonaTemplate: "lighthouse"})},
		{Bot: PanelBotConfig(store.PanelBot{ID: "panel-b2c3d4e5", Name: "小刚", PersonaTemplate: "default"})},
		{Bot: PanelBotConfig(store.PanelBot{ID: "panel-c3d4e5f6", Name: "小绿", PersonaTemplate: "lighthouse"})},
	})
	if got := d.PanelTemplateUsers("lighthouse"); !slices.Equal(got, []string{"小红", "小绿"}) {
		t.Fatalf("PanelTemplateUsers(lighthouse) = %v, want the panel bots 小红 and 小绿, not the config bot", got)
	}
	if got := d.PanelTemplateUsers("cat"); len(got) != 0 {
		t.Fatalf("PanelTemplateUsers(cat) = %v, want none", got)
	}
}

func TestPanelBotConfig(t *testing.T) {
	skills := []string{"memory"}
	b := PanelBotConfig(store.PanelBot{ID: "panel-a1b2c3d4", Name: "小明", PersonaTemplate: "default", Skills: skills})
	want := config.BotConfig{ID: "panel-a1b2c3d4", Platform: store.PlatformPanel, Name: "小明", PrivateChat: "admins",
		PersonaTemplate: "default", Skills: []string{"memory"}}
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
	snapshot := func(gen int) []PanelEntry {
		name := fmt.Sprintf("第%d版", gen)
		return []PanelEntry{
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
