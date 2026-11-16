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

func TestValidBarName(t *testing.T) {
	for name, want := range map[string]bool{
		"心情": true, "六个字的名字": true, "好奇!": true,
		"": false, "七个字的名字呀": false, "心 情": false, " 心情": false, "心\u3000情": false, "心\t情": false, "心情\n": false,
		"心\u200b情": false, "【心情】": false, "心情：": false, "心:情": false,
		".": false, "..": false, "......": false, "心情.": true, "..心": true,
	} {
		if got := ValidBarName(name); got != want {
			t.Errorf("ValidBarName(%q) = %v, want %v", name, got, want)
		}
		p := DefaultPersona()
		p.Bars = []PersonaBar{{Name: name, Baseline: 50, HalfLife: Duration{time.Hour}}}
		if valid := p.Validate() == nil; valid != want {
			t.Errorf("Validate with bar %q: valid %v, want %v like ValidBarName", name, valid, want)
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

const testImageID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestPersona_AppearanceValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*PersonaDoc)
		wantErr string
	}{
		{"no appearance", func(*PersonaDoc) {}, ""},
		{"appearance 800 runes", func(p *PersonaDoc) { p.Appearance = strings.Repeat("貌", 800) }, ""},
		{"appearance 800 runes and blanks", func(p *PersonaDoc) { p.Appearance = " \n" + strings.Repeat("貌", 800) + "\n" }, ""},
		{"appearance 801 runes", func(p *PersonaDoc) { p.Appearance = strings.Repeat("貌", 801) }, "appearance must be at most 800 characters, got 801"},
		{"image id", func(p *PersonaDoc) { p.AppearanceImage = testImageID }, ""},
		{"image id and skip", func(p *PersonaDoc) { p.AppearanceImage, p.AppearanceImageSkip = testImageID, true }, ""},
		{"skip without image", func(p *PersonaDoc) { p.AppearanceImageSkip = true }, ""},
		{"image id 63 chars", func(p *PersonaDoc) { p.AppearanceImage = testImageID[1:] }, "appearance_image must be"},
		{"image id 65 chars", func(p *PersonaDoc) { p.AppearanceImage = testImageID + "0" }, "appearance_image must be"},
		{"image id uppercase", func(p *PersonaDoc) { p.AppearanceImage = strings.ToUpper(testImageID) }, "appearance_image must be"},
		{"image id not hex", func(p *PersonaDoc) { p.AppearanceImage = "g" + testImageID[1:] }, "appearance_image must be"},
		{"image id with blanks", func(p *PersonaDoc) { p.AppearanceImage = " " + testImageID[1:] }, "appearance_image must be"},
		{"image id blank", func(p *PersonaDoc) { p.AppearanceImage = " " }, "appearance_image must be"},
		{"image id path", func(p *PersonaDoc) { p.AppearanceImage = "../" + testImageID[3:] }, "appearance_image must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := DefaultPersona()
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

	p := DefaultPersona()
	p.Style.Humor = -1
	p.Appearance = strings.Repeat("貌", 801)
	p.AppearanceImage = "not-an-id"
	err := p.Validate()
	if err == nil {
		t.Fatal("Validate = nil, want errors")
	}
	for _, want := range []string{"style.humor", "appearance must be at most 800", "appearance_image must be"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to contain %q", err, want)
		}
	}
}

func TestPersona_AppearanceJSONAndYAML(t *testing.T) {
	const legacyJSON = `{"persona_name":"默认群友人格","template_version":1,` +
		`"style":{"warmth":0.75,"humor":0.45,"directness":0.65,"verbosity":0.3,"emoji_usage":0.15,"proactivity":0.2},` +
		`"behavior":{"default_reply_length":"short","allow_memes":true,"ask_followup_questions":"occasionally","technical_explanations":"clear_and_practical"}}`
	b, err := json.Marshal(DefaultPersona())
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != legacyJSON {
		t.Errorf("JSON of DefaultPersona =\n%s\nwant\n%s", b, legacyJSON)
	}
	full := DefaultPersona()
	full.CharacterName, full.Description, full.SpeakingStyle = "小明", "小明是一名学生。", "说话简短。"
	full.Examples = []PersonaExample{{User: "在吗", Reply: "在"}}
	full.Adult, full.ContentRules, full.MemoryRules, full.Bars = true, "规则", "记忆", testBars()
	b, err = json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "appearance") {
		t.Errorf("persona without appearance encodes an appearance key: %s", b)
	}
	y, err := yaml.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(y), "appearance") {
		t.Errorf("persona without appearance encodes an appearance key in YAML: %s", y)
	}

	p := DefaultPersona()
	p.Appearance = "短发，常穿蓝色外套。"
	p.AppearanceImage = testImageID
	p.AppearanceImageSkip = true
	b, err = json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := `,"appearance":"短发，常穿蓝色外套。","appearance_image":"` + testImageID + `","appearance_image_skip":true`; !strings.Contains(string(b), want) {
		t.Errorf("JSON %s missing %s", b, want)
	}
	var back PersonaDoc
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, p) {
		t.Errorf("JSON round trip = %+v, want %+v", back, p)
	}

	var doc PersonaDoc
	src := "persona_name: 测试人格\nappearance: |\n  短发，常穿蓝色外套。\nappearance_image: " + testImageID + "\nappearance_image_skip: true\n"
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Appearance != "短发，常穿蓝色外套。\n" || doc.AppearanceImage != testImageID || !doc.AppearanceImageSkip {
		t.Errorf("YAML appearance fields = %q, %q, %v", doc.Appearance, doc.AppearanceImage, doc.AppearanceImageSkip)
	}
}

func TestPersona_GenderValidate(t *testing.T) {
	for _, g := range []string{"", GenderFemale, GenderMale} {
		p := DefaultPersona()
		p.Gender = g
		if err := p.Validate(); err != nil {
			t.Errorf("gender %q: unexpected error: %v", g, err)
		}
	}
	if GenderFemale != "female" || GenderMale != "male" {
		t.Errorf("GenderFemale, GenderMale = %q, %q, want female, male", GenderFemale, GenderMale)
	}
	for _, g := range []string{"Female", "MALE", " female", "male\n", "女", "男", "other", "unknown"} {
		p := DefaultPersona()
		p.Gender = g
		want := `gender must be one of female|male or empty, got "` + strings.ReplaceAll(g, "\n", `\n`) + `"`
		if err := p.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("gender %q: err = %v, want containing %q", g, err, want)
		}
	}

	p := DefaultPersona()
	p.Style.Humor = -1
	p.Gender = "other"
	err := p.Validate()
	for _, want := range []string{"style.humor", "gender must be"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to contain %q", err, want)
		}
	}
}

func TestPersona_GenderJSONAndYAML(t *testing.T) {
	b, err := json.Marshal(DefaultPersona())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "gender") {
		t.Errorf("persona without gender encodes a gender key: %s", b)
	}
	y, err := yaml.Marshal(DefaultPersona())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(y), "gender") {
		t.Errorf("persona without gender encodes a gender key in YAML: %s", y)
	}

	p := DefaultPersona()
	p.CharacterName, p.Gender = "小红", GenderFemale
	b, err = json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"character_name":"小红","gender":"female"`; !strings.Contains(string(b), want) {
		t.Errorf("JSON %s missing %s", b, want)
	}
	var back PersonaDoc
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, p) {
		t.Errorf("JSON round trip = %+v, want %+v", back, p)
	}

	var doc PersonaDoc
	if err := yaml.Unmarshal([]byte("persona_name: 测试人格\ngender: male\n"), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Gender != GenderMale {
		t.Errorf("YAML gender = %q, want %q", doc.Gender, GenderMale)
	}
}

func TestPersonaIntimacy_LevelAtDefaults(t *testing.T) {
	in := &PersonaIntimacy{Tone: "x"}
	for tier, want := range []string{IntimacyNone, IntimacyNone, IntimacyLight, IntimacyHalf, IntimacyMajor} {
		if got := in.LevelAt(tier); got != want {
			t.Errorf("LevelAt(%d) = %q, want %q", tier, got, want)
		}
	}
	for _, tier := range []int{5, -1} {
		if got := in.LevelAt(tier); got != IntimacyNone {
			t.Errorf("LevelAt(%d) = %q, want none", tier, got)
		}
	}
	custom := &PersonaIntimacy{Levels: []string{IntimacyLight, IntimacyHalf, IntimacyHalf, IntimacyMajor, IntimacyMajor}}
	if got := custom.LevelAt(0); got != IntimacyLight {
		t.Errorf("custom LevelAt(0) = %q, want light", got)
	}
	if got := (*PersonaIntimacy)(nil).LevelAt(4); got != IntimacyMajor {
		t.Errorf("nil LevelAt(4) = %q, want the default major", got)
	}

	if !(*PersonaIntimacy)(nil).Empty() {
		t.Error("nil Empty() = false, want true")
	}
	if !(&PersonaIntimacy{Tone: "  \n"}).Empty() {
		t.Error(`{Tone: "  \n"} Empty() = false, want true`)
	}
	if !(&PersonaIntimacy{Levels: DefaultIntimacyLevels()}).Empty() {
		t.Error("levels only: Empty() = false, want true")
	}
	if (&PersonaIntimacy{Behavior: " 牵手 "}).Empty() {
		t.Error("behavior set: Empty() = true, want false")
	}

	d := DefaultIntimacyLevels()
	d[0] = IntimacyMajor
	if got := DefaultIntimacyLevels(); got[0] != IntimacyNone {
		t.Errorf("DefaultIntimacyLevels after changing a returned slice = %v, want a fresh slice", got)
	}
}

func TestPersonaDocValidate_Intimacy(t *testing.T) {
	levels := func(l ...string) *PersonaIntimacy { return &PersonaIntimacy{Tone: "说话放软一些", Levels: l} }
	tests := []struct {
		name     string
		intimacy *PersonaIntimacy
		want     []string
	}{
		{"nil", nil, nil},
		{"800 runes", &PersonaIntimacy{
			Tone:     " " + strings.Repeat("语", 300) + "\n",
			Mind:     strings.Repeat("心", 150) + "\n" + strings.Repeat("想", 149),
			Behavior: "\t" + strings.Repeat("行", 200) + "  ",
		}, nil},
		{"801 runes", &PersonaIntimacy{
			Tone:     strings.Repeat("语", 300),
			Mind:     strings.Repeat("心", 300),
			Behavior: " " + strings.Repeat("行", 201) + "\n",
		}, []string{"intimacy", "800", "got 801"}},
		{"levels omitted", levels(), nil},
		{"levels all none", levels(IntimacyNone, IntimacyNone, IntimacyNone, IntimacyNone, IntimacyNone), nil},
		{"levels all major", levels(IntimacyMajor, IntimacyMajor, IntimacyMajor, IntimacyMajor, IntimacyMajor), nil},
		{"levels default", levels(DefaultIntimacyLevels()...), nil},
		{"levels only, texts empty", &PersonaIntimacy{Levels: DefaultIntimacyLevels()}, nil},
		{"3 levels", levels(IntimacyNone, IntimacyLight, IntimacyHalf), []string{"intimacy.levels", "5", "got 3"}},
		{"6 levels", levels(IntimacyNone, IntimacyNone, IntimacyLight, IntimacyHalf, IntimacyMajor, IntimacyMajor),
			[]string{"intimacy.levels", "got 6"}},
		{"unknown level", levels(IntimacyNone, IntimacyNone, IntimacyLight, IntimacyHalf, "full"),
			[]string{"intimacy.levels[4]", `"full"`}},
		{"empty level", levels(IntimacyNone, "", IntimacyLight, IntimacyHalf, IntimacyMajor), []string{"intimacy.levels[1]"}},
		{"decreasing", levels(IntimacyNone, IntimacyLight, IntimacyNone, IntimacyHalf, IntimacyMajor), []string{"intimacy.levels[2]"}},
		{"decreasing at the top", levels(IntimacyNone, IntimacyNone, IntimacyLight, IntimacyMajor, IntimacyHalf),
			[]string{"intimacy.levels[4]"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := DefaultPersona()
			p.Intimacy = tt.intimacy
			err := p.Validate()
			if len(tt.want) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			for _, w := range tt.want {
				if err == nil || !strings.Contains(err.Error(), w) {
					t.Errorf("err = %v, want containing %q", err, w)
				}
			}
		})
	}

	p := DefaultPersona()
	p.Style.Warmth = 2
	p.Intimacy = &PersonaIntimacy{Mind: strings.Repeat("心", 801), Levels: []string{IntimacyMajor, "full", IntimacyNone}}
	err := p.Validate()
	for _, want := range []string{"style.warmth", "intimacy must be", "intimacy.levels must", "intimacy.levels[1]"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to contain %q", err, want)
		}
	}
}

func TestPersonaIntimacy_JSONAndYAML(t *testing.T) {
	b, err := json.Marshal(DefaultPersona())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "intimacy") {
		t.Errorf("persona without an intimacy side encodes an intimacy key: %s", b)
	}

	p := DefaultPersona()
	p.ContentRules = "规则"
	p.Intimacy = &PersonaIntimacy{Tone: "说话放软一些", Levels: DefaultIntimacyLevels()}
	b, err = json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"content_rules":"规则","intimacy":{"tone":"说话放软一些","levels":["none","none","light","half","major"]}`; !strings.Contains(string(b), want) {
		t.Errorf("JSON %s missing %s", b, want)
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
intimacy:
  tone: 说话放软一些
  mind: |
    想靠近对方
  behavior: 会多陪对方聊一会儿
  levels: [none, light, light, half, major]
`
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	want := &PersonaIntimacy{Tone: "说话放软一些", Mind: "想靠近对方\n", Behavior: "会多陪对方聊一会儿",
		Levels: []string{IntimacyNone, IntimacyLight, IntimacyLight, IntimacyHalf, IntimacyMajor}}
	if !reflect.DeepEqual(doc.Intimacy, want) {
		t.Errorf("YAML intimacy = %+v, want %+v", doc.Intimacy, want)
	}
}
