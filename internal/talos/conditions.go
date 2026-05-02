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