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

func Test_Store_refuses_to_arm_while_a_spend_has_no_recorded_result(t *testing.T) {
	// Given a spend that began and has not been recorded as finished
	store := newTestStore(t)
	request := ArmRequest{AccountFingerprint: testFingerprint, WindowEndsAt: testWindow(), Kinds: []string{"fable"}}
	armed, err := store.Arm(context.Background(), ProviderClaude, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginFire(context.Background(), ProviderClaude, armed, "fable"); err != nil {
		t.Fatal(err)
	}

	// When a new authorization is requested during it
	_, err = store.Arm(context.Background(), ProviderClaude, request)

	// Then it is refused and nothing is armed
	if !errors.Is(err, ErrSpendInProgress) {
		t.Fatalf("Arm error = %v, want ErrSpendInProgress", err)
	}
	loaded, loadErr := store.Load()
	if loadErr != nil || loaded[ProviderClaude].Armed() {
		t.Fatalf("an authorization was stored during a spend: %+v, error=%v", loaded[ProviderClaude], loadErr)
	}

	// And a recorded result, or an abandoned spend past its time budget, allows arming again
	store.now = func() time.Time { return testNow().Add(2*resetBudget - time.Second) }
	if _, err := store.Arm(context.Background(), ProviderClaude, request); !errors.Is(err, ErrSpendInProgress) {
		t.Fatalf("Arm just inside the budget = %v, want ErrSpendInProgress", err)
	}
	store.now = func() time.Time { return testNow().Add(2 * resetBudget) }
	if _, err := store.Arm(context.Background(), ProviderClaude, request); err != nil {
		t.Fatalf("Arm after the abandoned spend's budget = %v", err)
	}
}

func Test_Store_allows_arming_once_a_spend_result_is_recorded(t *testing.T) {
	// Given
	store := newTestStore(t)
	request := ArmRequest{AccountFingerprint: testFingerprint, WindowEndsAt: testWindow(), Kinds: []string{"fable"}}
	armed, err := store.Arm(context.Background(), ProviderClaude, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginFire(context.Background(), ProviderClaude, armed, "fable"); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishFire(context.Background(), ProviderClaude, newEvent(testNow(), "completed", "reset", "", "fable")); err != nil {
		t.Fatal(err)
	}

	// When
	_, err = store.Arm(context.Background(), ProviderClaude, request)

	// Then
	if err != nil {
		t.Fatalf("Arm after a recorded result = %v", err)
	}
}
