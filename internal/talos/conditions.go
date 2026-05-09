package talos

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func HasCondition(conditions []metav1.Condition, conditionType string, status metav1.ConditionStatus) bool {
	for _, c := range conditions {
		if string(c.Type) == conditionType && c.Status == status {
			return true
		}
	}
	return false
}

// SetConditionStatus is a convenience wrapper around SetCondition for the common
// case of setting Type, Status, Reason, and Message together.
func SetConditionStatus(conditions *[]metav1.Condition, condType string, status metav1.ConditionStatus, reason, message string) {
	SetCondition(conditions, metav1.Condition{
		Type:    condType,
		Status:  status,
		Reason:  reason,
		Message: message,
	})
}

func SetCondition(conditions *[]metav1.Condition, condition metav1.Condition) {
	now := metav1.Now()

	for i, c := range *conditions {
		if c.Type != condition.Type {
			continue
		}
		if c.Status == condition.Status {
			condition.LastTransitionTime = c.LastTransitionTime
		} else {
			condition.LastTransitionTime = now
		}
		(*conditions)[i] = condition
		return
	}
	condition.LastTransitionTime = now
	*conditions = append(*conditions, condition)
}