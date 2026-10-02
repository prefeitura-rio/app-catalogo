package datasource

import (
	"testing"

	"github.com/prefeitura-rio/app-catalogo/internal/clients"
)

func TestCourseIsIndexable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status string
		visible bool
		want   bool
	}{
		{"published visible", "published", true, true},
		{"approved visible", "approved", true, true},
		{"opened visible", "opened", true, true},
		{"empty status visible", "", true, true},
		{"scheduled derived", "scheduled", true, true},
		{"accepting_enrollments derived", "accepting_enrollments", true, true},
		{"in_progress derived", "in_progress", true, true},
		{"finished terminal", "finished", true, false},
		{"draft", "draft", true, false},
		{"canceled", "canceled", true, false},
		{"published invisible", "published", false, false},
		{"accepting invisible", "accepting_enrollments", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := courseIsIndexable(clients.Course{
				Status:    tt.status,
				IsVisible: tt.visible,
			})
			if got != tt.want {
				t.Fatalf("courseIsIndexable(status=%q, visible=%v) = %v, want %v",
					tt.status, tt.visible, got, tt.want)
			}
		})
	}
}
