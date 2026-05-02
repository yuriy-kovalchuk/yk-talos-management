package talos

import "testing"

func TestContainsFinalizer(t *testing.T) {
	list := []string{"controller1", FinalizerCleanup, "controller2"}

	tests := []struct {
		name    string
		list    []string
		finalizer string
		want    bool
	}{
		{
			name:     "finds finalizer",
			list:     list,
			finalizer: FinalizerCleanup,
			want:     true,
		},
		{
			name:     "not found",
			list:     []string{"other"},
			finalizer: FinalizerCleanup,
			want:     false,
		},
		{
			name:     "empty list",
			list:     nil,
			finalizer: FinalizerCleanup,
			want:     false,
		},
		{
			name:     "empty string matches",
			list:     []string{""},
			finalizer: "",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainsFinalizer(tt.list, tt.finalizer)
			if got != tt.want {
				t.Errorf("ContainsFinalizer() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAddFinalizer(t *testing.T) {
	t.Run("appends if not present", func(t *testing.T) {
		list := []string{"controller1", "controller2"}
		listPtr := &list

		AddFinalizer(listPtr, FinalizerCleanup)

		if len(*listPtr) != 3 {
			t.Errorf("expected length 3, got %d", len(*listPtr))
		}
		if (*listPtr)[2] != FinalizerCleanup {
			t.Errorf("expected finalizer at index 2, got %s", (*listPtr)[2])
		}
	})

	t.Run("no-op if already present", func(t *testing.T) {
		list := []string{"controller1", FinalizerCleanup, "controller2"}
		listPtr := &list

		AddFinalizer(listPtr, FinalizerCleanup)

		if len(*listPtr) != 3 {
			t.Errorf("expected length 3, got %d", len(*listPtr))
		}
	})

	t.Run("handles nil slice", func(t *testing.T) {
		var list []string
		listPtr := &list

		AddFinalizer(listPtr, FinalizerCleanup)

		if len(*listPtr) != 1 {
			t.Errorf("expected length 1, got %d", len(*listPtr))
		}
	})
}

func TestRemoveFinalizer(t *testing.T) {
	t.Run("removes finalizer", func(t *testing.T) {
		list := []string{"controller1", FinalizerCleanup, "controller2"}

		got := RemoveFinalizer(list, FinalizerCleanup)

		if len(got) != 2 {
			t.Errorf("expected length 2, got %d", len(got))
		}
		if got[0] != "controller1" || got[1] != "controller2" {
			t.Errorf("unexpected result: %v", got)
		}
	})

	t.Run("returns copy if not found", func(t *testing.T) {
		list := []string{"controller1", "controller2"}

		got := RemoveFinalizer(list, FinalizerCleanup)

		if len(got) != 2 {
			t.Errorf("expected length 2, got %d", len(got))
		}
	})

	t.Run("handles empty list", func(t *testing.T) {
		list := []string{}

		got := RemoveFinalizer(list, FinalizerCleanup)

		if len(got) != 0 {
			t.Errorf("expected length 0, got %d", len(got))
		}
	})

	t.Run("removes all occurrences", func(t *testing.T) {
		list := []string{FinalizerCleanup, FinalizerCleanup, "controller1"}

		got := RemoveFinalizer(list, FinalizerCleanup)

		if len(got) != 1 {
			t.Errorf("expected length 1, got %d", len(got))
		}
		if got[0] != "controller1" {
			t.Errorf("expected controller1, got %v", got)
		}
	})
}