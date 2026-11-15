package credstore

import (
	"errors"
	"os"
	"time"
)

var (
	ErrInvalidJSON = errors.New("credstore: invalid json")
	ErrNilMutator  = errors.New("credstore: nil mutator")
	ErrNotWritable = errors.New("credstore: credentials cannot be written")
)

type Snapshot struct {
	Data    []byte
	ModTime time.Time
	Mode    os.FileMode
}

type Mutator func([]byte) ([]byte, error)

type RefreshDecision int

const (
	RefreshDecisionUseSnapshot RefreshDecision = iota
	RefreshDecisionReload
	RefreshDecisionRefresh
)

type RefreshState struct {
	Snapshot       Snapshot
	CurrentModTime time.Time
	ExpiresAt      time.Time
}

func ShouldRefresh(now time.Time, state RefreshState) RefreshDecision {
	if !state.CurrentModTime.Equal(state.Snapshot.ModTime) {
		return RefreshDecisionReload
	}
	if !now.Before(state.ExpiresAt) {
		return RefreshDecisionRefresh
	}
	return RefreshDecisionUseSnapshot
}
