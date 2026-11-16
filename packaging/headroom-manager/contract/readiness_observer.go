package contract

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type readinessObservation struct {
	path     string
	expected readinessMarker
	exited   <-chan processExit
}

func (observation readinessObservation) wait(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case exit := <-observation.exited:
			return fmt.Errorf("replacement app exited before readiness: %v", exit)
		default:
		}
		data, err := readBoundedFile(observation.path, 4096)
		var marker readinessMarker
		if err == nil && json.Unmarshal(data, &marker) == nil {
			expected := observation.expected
			if expected.PID == 0 {
				expected.PID = marker.PID
			}
			if matchesReadinessMarker(data, expected) {
				token, tokenErr := captureProcessToken(marker.PID, marker.Executable)
				if tokenErr == nil {
					timer := time.NewTimer(200 * time.Millisecond)
					select {
					case <-ctx.Done():
						timer.Stop()
						return ctx.Err()
					case exit := <-observation.exited:
						timer.Stop()
						return fmt.Errorf("replacement app exited during readiness: %v", exit)
					case <-timer.C:
					}
					got, captureErr := captureProcessToken(marker.PID, marker.Executable)
					if captureErr == nil && token == got && !processGone(marker.PID) {
						return nil
					}
				}
			}
		}
		if err = observePause(ctx); err != nil {
			return err
		}
	}
}
