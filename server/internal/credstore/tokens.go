package credstore

import (
	"context"
	"errors"
	"os"
)

// TokenPair is the access and refresh token a credentials store holds.
type TokenPair struct {
	Access  string
	Refresh string
}

// TokenCodec describes one store's token pair. Pair and Apply are pure and must
// not write the store; Of maps a refresh result to the pair the store will
// carry. Warn, when set, receives a commit error whose write may still have
// landed so a provider can log that a refreshed sign-in was lost.
type TokenCodec[T any] struct {
	Pair  func(file []byte) (TokenPair, error)
	Apply func(file []byte, tokens T) ([]byte, error)
	Of    func(tokens T) TokenPair
	Warn  func(error)
}

// Outcome reports what RefreshTokens did with the store.
type Outcome int

const (
	// Adopted means the file already held a pair other than spent; the result
	// either was not exchanged or was superseded and discarded.
	Adopted Outcome = iota
	// Saved means the refreshed tokens were written, or already present.
	Saved
)

// ErrTokenCodec reports a TokenCodec missing a required function.
var ErrTokenCodec = errors.New("credstore: incomplete token codec")

// RefreshTokens refreshes path when it still holds spent and saves the result
// through codec. It is the one owner of the spent-pair rule: exchange runs at
// most once per call, only while the file still holds spent, and must not
// write the store. A file that already holds another pair is adopted without
// an exchange, and a result superseded while the exchange ran is discarded.
func RefreshTokens[T any](ctx context.Context, path string, spent TokenPair, codec TokenCodec[T],
	exchange func(file []byte) (T, error)) (result T, outcome Outcome, err error) {
	if codec.Pair == nil || codec.Apply == nil || codec.Of == nil {
		return result, Adopted, ErrTokenCodec
	}
	obtained := false
	outcome = Adopted
	err = Refresh(ctx, path, func(current []byte) (Mutator, error) {
		pair, pairErr := codec.Pair(current)
		if pairErr != nil {
			return nil, pairErr
		}
		if pair != spent {
			return nil, nil
		}
		tokens, exchangeErr := exchange(current)
		if exchangeErr != nil {
			return nil, exchangeErr
		}
		result, obtained = tokens, true
		wanted := codec.Of(tokens)
		return func(latest []byte) ([]byte, error) {
			latestPair, latestErr := codec.Pair(latest)
			if latestErr != nil {
				return nil, latestErr
			}
			switch latestPair {
			case spent:
				outcome = Saved
				return codec.Apply(latest, tokens)
			case wanted:
				outcome = Saved
				return latest, nil
			default:
				outcome = Adopted
				return latest, nil
			}
		}, nil
	})
	if err == nil {
		return result, outcome, nil
	}
	if !obtained {
		return result, Adopted, err
	}
	// A commit error can follow a successful rename (directory sync or unlock
	// failed). The successor on disk is what matters.
	if data, readErr := os.ReadFile(path); readErr == nil {
		if pair, pairErr := codec.Pair(data); pairErr == nil && pair == codec.Of(result) {
			if codec.Warn != nil {
				codec.Warn(err)
			}
			return result, Saved, nil
		}
	}
	return result, Adopted, err
}
