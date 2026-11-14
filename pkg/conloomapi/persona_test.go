package conloomapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func testBars() []PersonaBar {
	return []PersonaBar{
		{Name: "心情", Baseline: 60, HalfLife: Duration{6 * time.Hour}, Description: "高=开心放松，低=低落烦躁"},
		{Name: "精力", Baseline: 70, HalfLife: Duration{4 * time.Hour}},
	}
}

func TestPersonaBars_Validate(t *testing.T) {
	bar := func(name string) PersonaBar {
		return PersonaBar{Name: name, Baseline: 50, HalfLife: Duration{time.Hour}}
	}
	tests := []struct {
		name    string
		mutate  func(*PersonaDoc)
		wantErr string
	}{
		{"no bars", func(p *PersonaDoc) { p.Bars = nil }, ""},
		{"two bars", func(*PersonaDoc) {}, ""},
		{"default bars", func(p *PersonaDoc) { p.Bars = DefaultBars() }, ""},
		{"four bars", func(p *PersonaDoc) { p.Bars = []PersonaBar{bar("甲"), bar("乙"), bar("丙"), bar("丁")} }, ""},
		{"five bars", func(p *PersonaDoc) {
			p.Bars = []PersonaBar{bar("甲"), bar("乙"), bar("丙"), bar("丁"), bar("戊")}
		}, "bars must have at most 4 items, got 5"},
		{"name 6 runes", func(p *PersonaDoc) { p.Bars[0].Name = "一二三四五六" }, ""},
		{"name latin", func(p *PersonaDoc) { p.Bars[0].Name = "mood" }, ""},
		{"name empty", func(p *PersonaDoc) { p.Bars[0].Name = "" }, "bars[0].name must not be empty"},
		{"name 7 runes", func(p *PersonaDoc) { p.Bars[1].Name = "一二三四五六七" }, "bars[1].name must be at most 6 characters, got 7"},
		{"name with space", func(p *PersonaDoc) { p.Bars[0].Name = "心 情" }, "bars[0].name must not contain"},
		{"name with leading space", func(p *PersonaDoc) { p.Bars[0].Name = " 心情" }, "bars[0].name must not contain"},
		{"name with ideographic space", func(p *PersonaDoc) { p.Bars[0].Name = "心\u3000情" }, "bars[0].name must not contain"},
		{"name with tab", func(p *PersonaDoc) { p.Bars[0].Name = "心\t情" }, "bars[0].name must not contain"},
		{"name with newline", func(p *PersonaDoc) { p.Bars[0].Name = "心情\n" }, "bars[0].name must not contain"},
		{"name with zero-width space", func(p *PersonaDoc) { p.Bars[0].Name = "心\u200b情" }, "bars[0].name must not contain"},
		{"name with open bracket", func(p *PersonaDoc) { p.Bars[1].Name = "【精力" }, "bars[1].name must not contain"},
		{"name with close bracket", func(p *PersonaDoc) { p.Bars[1].Name = "精力】" }, "bars[1].name must not contain"},
		{"name with full-width colon", func(p *PersonaDoc) { p.Bars[0].Name = "心情：" }, "bars[0].name must not contain"},
		{"name with colon", func(p *PersonaDoc) { p.Bars[0].Name = "心:情" }, "bars[0].name must not contain"},
		{"duplicate names", func(p *PersonaDoc) { p.Bars[1].Name = "心情" }, "bars[1].name duplicates bars[0].name"},
		{"baseline 0", func(p *PersonaDoc) { p.Bars[0].Baseline = 0 }, ""},
		{"baseline 100", func(p *PersonaDoc) { p.Bars[0].Baseline = 100 }, ""},
		{"baseline -1", func(p *PersonaDoc) { p.Bars[0].Baseline = -1 }, "bars[0].baseline must be in [0, 100], got -1"},
		{"baseline 101", func(p *PersonaDoc) { p.Bars[1].Baseline = 101 }, "bars[1].baseline must be in [0, 100], got 101"},
		{"half-life 30m", func(p *PersonaDoc) { p.Bars[0].HalfLife = Duration{30 * time.Minute} }, ""},
		{"half-life 7d", func(p *PersonaDoc) { p.Bars[0].HalfLife = Duration{7 * 24 * time.Hour} }, ""},
		{"half-life 29m", func(p *PersonaDoc) { p.Bars[0].HalfLife = Duration{29 * time.Minute} }, "bars[0].half_life must be in [30m, 168h], got 29m0s"},
		{"half-life 8d", func(p *PersonaDoc) { p.Bars[1].HalfLife = Duration{8 * 24 * time.Hour} }, "bars[1].half_life must be in [30m, 168h], got 192h0m0s"},
		{"half-life missing", func(p *PersonaDoc) { p.Bars[0].HalfLife = Duration{} }, "bars[0].half_life must be in [30m, 168h], got 0s"},
		{"description empty", func(p *PersonaDoc) { p.Bars[0].Description = "" }, ""},
		{"description 40 runes", func(p *PersonaDoc) { p.Bars[0].Description = strings.Repeat("说", 40) }, ""},
		{"description 41 runes", func(p *PersonaDoc) { p.Bars[0].Description = strings.Repeat("说", 41) }, "bars[0].description must be at most 40 characters, got 41"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := DefaultPersona()
			p.Bars = testBars()
			tt.mutate(&p)
			err := p.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestPersonaBars_ValidateCollectsEveryError(t *testing.T) {
	p := DefaultPersona()
	p.Style.Warmth = 2
	p.Bars = []PersonaBar{
		{Name: "", Baseline: -1, HalfLife: Duration{time.Minute}, Description: strings.Repeat("说", 41)},
		{Name: "精力", Baseline: 70, HalfLife: Duration{4 * time.Hour}},
		{Name: "精力", Baseline: 101, HalfLife: Duration{4 * time.Hour}},
	}
	err := p.Validate()
	if err == nil {
		t.Fatal("Validate = nil, want errors")
	}
	for _, want := range []string{
		"style.warmth", "bars[0].name", "bars[0].baseline", "bars[0].half_life", "bars[0].description",
		"bars[2].name duplicates bars[1].name", "bars[2].baseline",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to contain %q", err, want)
		}
	}
}

func TestPersonaBars_Effective(t *testing.T) {
	want := []PersonaBar{
		{Name: "心情", Baseline: 60, HalfLife: Duration{6 * time.Hour}, Description: "高=开心放松，低=低落烦躁"},
		{Name: "精力", Baseline: 70, HalfLife: Duration{4 * time.Hour}, Description: "高=有精神，低=累、困"},
	}
	if got := DefaultBars(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultBars = %+v, want %+v", got, want)
	}
	DefaultBars()[0].Name = "改了"
	if got := DefaultBars(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultBars after changing a returned slice = %+v, want %+v", got, want)
	}

	if bars := DefaultPersona().Bars; bars != nil {
		t.Errorf("DefaultPersona().Bars = %+v, want nil", bars)
	}
	if got := DefaultPersona().EffectiveBars(); !reflect.DeepEqual(got, want) {
		t.Errorf("EffectiveBars without bars = %+v, want the defaults", got)
	}
	empty := DefaultPersona()
	empty.Bars = []PersonaBar{}
	if got := empty.EffectiveBars(); !reflect.DeepEqual(got, want) {
		t.Errorf("EffectiveBars with an empty list = %+v, want the defaults", got)
	}
	set := DefaultPersona()
	set.Bars = []PersonaBar{{Name: "好奇", Baseline: 40, HalfLife: Duration{12 * time.Hour}}}
	if got := set.EffectiveBars(); !reflect.DeepEqual(got, set.Bars) {
		t.Errorf("EffectiveBars = %+v, want the persona's bars %+v", got, set.Bars)
	}
	set.EffectiveBars()[0].Name = "改了"
	if set.Bars[0].Name != "好奇" {
		t.Errorf("persona's bars after changing EffectiveBars' result = %+v, want them unchanged", set.Bars)
	}
}

func TestPersonaBars_JSONAndYAML(t *testing.T) {
	b, err := json.Marshal(DefaultPersona())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "bars") {
		t.Errorf("persona without bars encodes a bars key: %s", b)
	}

	p := DefaultPersona()
	p.Bars = testBars()
	b, err = json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"bars":[`,
		`{"name":"心情","baseline":60,"half_life":"6h0m0s","description":"高=开心放松，低=低落烦躁"}`,
		`{"name":"精力","baseline":70,"half_life":"4h0m0s"}`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON %s missing %s", b, want)
		}
	}
	var back PersonaDoc
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, p) {
		t.Errorf("JSON round trip = %+v, want %+v", back, p)
	}

	var doc PersonaDoc
	src := `
persona_name: 测试人格
bars:
  - name: 心情
    baseline: 60
    half_life: 6h
    description: 高=开心放松，低=低落烦躁
  - name: 精力
    baseline: 70
    half_life: 4h
`
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc.Bars, testBars()) {
		t.Errorf("YAML bars = %+v, want %+v", doc.Bars, testBars())
	}
}
