package cli

import (
	"encoding/json"
	"os"
	"testing"
)

func TestWarningHysteresisSharedFixture(t *testing.T) {
	data, err := os.ReadFile("../../../server/internal/usage/testdata/warning_hysteresis.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Schema int `json:"schema"`
		Cases  []struct {
			Name     string `json:"name"`
			Previous int    `json:"previous"`
			Steps    []struct {
				Used     float64  `json:"used"`
				Pressure *float64 `json:"pressure"`
				Expected int      `json:"expected"`
			} `json:"steps"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Schema != 1 || len(fixture.Cases) == 0 {
		t.Fatal("unsupported or empty warning hysteresis fixture")
	}
	for _, scenario := range fixture.Cases {
		t.Run(scenario.Name, func(t *testing.T) {
			previous := scenario.Previous
			if len(scenario.Steps) == 0 {
				t.Fatal("empty sequence")
			}
			for index, step := range scenario.Steps {
				pacing := pace{available: step.Pressure != nil}
				if step.Pressure != nil {
					pacing.pressure = *step.Pressure
				}
				if got := warningLevel(step.Used, pacing, previous); got != step.Expected {
					t.Errorf("step %d: used=%g pace=%+v previous=%d: got %d, want %d",
						index+1, step.Used, pacing, previous, got, step.Expected)
				}
				previous = step.Expected
			}
		})
	}
}
