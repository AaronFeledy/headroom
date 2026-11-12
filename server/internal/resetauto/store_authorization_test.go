package resetauto

import (
	"context"
	"errors"
	"testing"
	"time"
)

func Test_Store_evaluation_cancel_requires_the_observed_authorization(t *testing.T) {
	// Given
	store := newTestStore(t)
	observed, err := store.Arm(context.Background(), ProviderCodex, ArmRequest{AccountFingerprint: testFingerprint, WindowEndsAt: testWindow()})
	if err != nil {
		t.Fatal(err)
	}
	other := observed
	other.ArmedAt = observed.ArmedAt.Add(time.Second)

	// When
	if _, err := store.CancelIfEvaluation(context.Background(), ProviderCodex, other, ReasonWindowEnded); err != nil {
		t.Fatal(err)
	}

	// Then
	loaded, err := store.Load()
	if err != nil || !loaded[ProviderCodex].Armed() {
		t.Fatalf("a stale evaluation canceled the authorization: %+v, error=%v", loaded[ProviderCodex], err)
	}
	if _, err := store.CancelIfEvaluation(context.Background(), ProviderCodex, observed, ReasonWindowEnded); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load()
	if err != nil || loaded[ProviderCodex].Armed() || loaded[ProviderCodex].LastEvent == nil {
		t.Fatalf("evaluation cancel = %+v, error=%v", loaded[ProviderCodex], err)
	}
}

func Test_Store_replacement_in_the_same_second_is_a_different_authorization(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replace func(Record) Record
	}{
		{"different kinds", func(r Record) Record { r.Kinds = []string{"weekly"}; return r }},
		{"different window", func(r Record) Record { r.WindowEndsAt = r.WindowEndsAt.Add(7 * 24 * time.Hour); return r }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given an observer evaluated one authorization, and a different one
			// with the same stored time and account has since replaced it.
			store := newTestStore(t)
			stored, err := store.Arm(context.Background(), ProviderClaude, ArmRequest{AccountFingerprint: testFingerprint, WindowEndsAt: testWindow(), Kinds: []string{"fable"}})
			if err != nil {
				t.Fatal(err)
			}
			observed := tc.replace(stored)

			// When the observer tries to act on what it evaluated
			beginErr := store.BeginFire(context.Background(), ProviderClaude, observed, "fable")
			_, cancelErr := store.CancelIfEvaluation(context.Background(), ProviderClaude, observed, ReasonKindUnavailable)

			// Then neither the spend transition nor the cancel touches the stored one
			if !errors.Is(beginErr, ErrStaleAuthorization) {
				t.Fatalf("BeginFire error = %v, want ErrStaleAuthorization", beginErr)
			}
			if cancelErr != nil {
				t.Fatal(cancelErr)
			}
			loaded, err := store.Load()
			if err != nil || !loaded[ProviderClaude].Armed() || loaded[ProviderClaude].LastEvent != nil {
				t.Fatalf("stored authorization changed: %+v, error=%v", loaded[ProviderClaude], err)
			}
		})
	}
}
