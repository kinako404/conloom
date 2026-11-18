package conloomapi

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAutonomyScene_JSON(t *testing.T) {
	raw, err := json.Marshal(AutonomyScene{Enabled: true, Mode: SceneOnline})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"enabled":true,"mode":"online","place":"","idle_online":false}`; string(raw) != want {
		t.Errorf("online scene = %s, want %s", raw, want)
	}
	since := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	raw, err = json.Marshal(AutonomyScene{Enabled: true, Mode: SceneInPerson, Place: "食堂", SinceAt: since})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"enabled":true,"mode":"in_person","place":"食堂","since_at":"2026-11-18T12:00:00Z","idle_online":false}`; string(raw) != want {
		t.Errorf("in-person scene = %s, want %s", raw, want)
	}

	var put AutonomyScenePut
	if err := json.Unmarshal([]byte(`{"mode":"in_person","place":"食堂"}`), &put); err != nil {
		t.Fatal(err)
	}
	if put != (AutonomyScenePut{Mode: SceneInPerson, Place: "食堂"}) {
		t.Errorf("decoded put = %+v", put)
	}
	if ScenePlaceMaxRunes != 16 {
		t.Errorf("ScenePlaceMaxRunes = %d, want 16", ScenePlaceMaxRunes)
	}
}
