package talos

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHasCondition(t *testing.T) {
	conds := []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue},
		{Type: "Configured", Status: metav1.ConditionFalse},
	}

	tests := []struct {
		name       string
		conditions []metav1.Condition
		condition  string
		status    metav1.ConditionStatus
		want      bool
	}{
		{
			name:       "finds matching condition",
			conditions: conds,
			condition:  "Ready",
			status:    metav1.ConditionTrue,
			want:      true,
		},
		{
			name:       "wrong type returns false",
			conditions: conds,
			condition:  "NotFound",
			status:    metav1.ConditionTrue,
			want:      false,
		},
		{
			name:       "wrong status returns false",
			conditions: conds,
			condition:  "Ready",
			status:    metav1.ConditionFalse,
			want:      false,
		},
		{
			name:       "empty conditions returns false",
			conditions: nil,
			condition:  "Ready",
			status:    metav1.ConditionTrue,
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasCondition(tt.conditions, tt.condition, tt.status)
			if got != tt.want {
				t.Errorf("HasCondition() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSetCondition(t *testing.T) {
	t.Run("updates existing condition", func(t *testing.T) {
		conds := []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionFalse, Message: "initial"},
		}
		condsPtr := &conds

		condition := metav1.Condition{
			Type:   "Ready",
			Status: metav1.ConditionTrue,
		}
		SetCondition(condsPtr, condition)

		if len(*condsPtr) != 1 {
			t.Errorf("expected 1 condition, got %d", len(*condsPtr))
		}
		if (*condsPtr)[0].Status != metav1.ConditionTrue {
			t.Errorf("expected status True, got %v", (*condsPtr)[0].Status)
		}
		if (*condsPtr)[0].LastTransitionTime.IsZero() {
			t.Error("expected LastTransitionTime to be refreshed on update")
		}
	})

	t.Run("appends new condition", func(t *testing.T) {
		conds := []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue},
		}
		condsPtr := &conds

		condition := metav1.Condition{
			Type:   "Configured",
			Status: metav1.ConditionTrue,
		}
		SetCondition(condsPtr, condition)

		if len(*condsPtr) != 2 {
			t.Errorf("expected 2 conditions, got %d", len(*condsPtr))
		}
	})

	t.Run("sets timestamp", func(t *testing.T) {
		var conds []metav1.Condition
		condsPtr := &conds

		condition := metav1.Condition{
			Type:   "Ready",
			Status: metav1.ConditionTrue,
		}
		SetCondition(condsPtr, condition)

		if (*condsPtr)[0].LastTransitionTime.IsZero() {
			t.Error("expected LastTransitionTime to be set")
		}
	})

	t.Run("preserves LastTransitionTime when status unchanged", func(t *testing.T) {
		original := metav1.NewTime(time.Now().Add(-1 * time.Hour))
		conds := []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, LastTransitionTime: original, Message: "old"},
		}
		SetCondition(&conds, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Message: "updated"})

		if !conds[0].LastTransitionTime.Equal(&original) {
			t.Errorf("LastTransitionTime should be preserved when status is unchanged, got %v", conds[0].LastTransitionTime)
		}
		if conds[0].Message != "updated" {
			t.Errorf("Message should be updated even when status is unchanged")
		}
	})

	t.Run("updates LastTransitionTime when status changes", func(t *testing.T) {
		original := metav1.NewTime(time.Now().Add(-1 * time.Hour))
		conds := []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionFalse, LastTransitionTime: original},
		}
		SetCondition(&conds, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue})

		if conds[0].LastTransitionTime.Equal(&original) {
			t.Error("LastTransitionTime should be updated when status changes")
		}
	})

	t.Run("handles nil slice", func(t *testing.T) {
		var conds []metav1.Condition
		condsPtr := &conds

		condition := metav1.Condition{
			Type:   "Ready",
			Status: metav1.ConditionTrue,
		}
		SetCondition(condsPtr, condition)

		if len(*condsPtr) != 1 {
			t.Errorf("expected 1 condition, got %d", len(*condsPtr))
		}
	})
}