package persona

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestExchange_OuterBarFields(t *testing.T) {
	ctx := context.Background()
	svc := newServiceWith(t, newTestStore(t), nil)
	plain := tagged("甲")
	plain.Bars = conloomapi.DefaultBars()
	outer := tagged("乙")
	outer.Bars = []conloomapi.PersonaBar{
		conloomapi.DefaultBars()[0],
		{Name: "肚子", Baseline: 20, HalfLife: conloomapi.Duration{Duration: 2 * time.Hour}, Description: "高=很饱",
			Kind: conloomapi.BarKindOuter, MaxStep: 40, MaxDay: 100},
	}
	for name, doc := range map[string]conloomapi.PersonaDoc{"plain": plain, "outer": outer} {
		if err := svc.PutTemplate(ctx, name, doc, "panel:admin"); err != nil {
			t.Fatalf("PutTemplate %s: %v", name, err)
		}
	}

	f, err := svc.Export(ctx, []string{"plain"})
	if err != nil {
		t.Fatalf("Export plain: %v", err)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"bars":[`)) {
		t.Fatalf("export of plain %s has no bars", raw)
	}
	for _, key := range []string{`"kind"`, `"max_step"`, `"max_day"`} {
		if bytes.Contains(raw, []byte(key)) {
			t.Errorf("export of default bars carries %s: %s", key, raw)
		}
	}

	f, err = svc.Export(ctx, []string{"outer"})
	if err != nil {
		t.Fatalf("Export outer: %v", err)
	}
	raw, err = json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"description":"高=很饱","kind":"outer","max_step":40,"max_day":100}`)) {
		t.Fatalf("export of outer %s does not carry the kind and limits", raw)
	}
	var back ExchangeFile
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("decode of the export: %v", err)
	}
	st := newTestStore(t)
	other := newServiceWith(t, st, nil)
	if _, err := other.Import(ctx, back, ConflictFail, "panel:other"); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got, _ := other.Template("outer"); !reflect.DeepEqual(got, outer) {
		t.Errorf("imported outer = %+v, want %+v", got, outer)
	}

	bad := exchangeJSON([]string{`{"name":"hungry","persona":{"persona_name":"人格丙","style":{},"behavior":{` +
		`"default_reply_length":"short","ask_followup_questions":"never","technical_explanations":"brief"},` +
		`"bars":[{"name":"心情","baseline":60,"half_life":"6h"},` +
		`{"name":"肚子","baseline":20,"half_life":"2h","kind":"outer","max_step":101}]}}`})
	var badFile ExchangeFile
	if err := json.Unmarshal(bad, &badFile); err != nil {
		t.Fatalf("decode of a file with max_step 101: %v", err)
	}
	for _, mode := range []ConflictMode{ConflictFail, ConflictOverwrite, ConflictRename} {
		_, err := other.Import(ctx, badFile, mode, "panel:admin")
		if !errors.Is(err, ErrInvalidTemplate) || !strings.Contains(err.Error(), `templates[0] "hungry"`) ||
			!strings.Contains(err.Error(), "bars[1].max_step") {
			t.Errorf("Import (%s) of max_step 101 err = %v, want ErrInvalidTemplate about bars[1].max_step", mode, err)
		}
		checkUnchanged(t, st, other, []string{"outer"})
	}

	unknown := exchangeJSON([]string{`{"name":"hungry","persona":{"persona_name":"人格丙",` +
		`"bars":[{"name":"肚子","baseline":20,"half_life":"2h","max_steps":40}]}}`})
	if err := json.Unmarshal(unknown, &badFile); err == nil || !strings.Contains(err.Error(), `unknown field "max_steps"`) {
		t.Errorf("decode of a bar with max_steps err = %v, want unknown field", err)
	}
}
