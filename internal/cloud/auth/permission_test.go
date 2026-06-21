package auth

import "testing"

func TestPermissionHas(t *testing.T) {
	tests := []struct {
		name     string
		p        Permission
		required Permission
		want     bool
	}{
		{"read satisfies read", PermRead, PermRead, true},
		{"all satisfies read", PermAll, PermRead, true},
		{"all satisfies insert", PermAll, PermInsert, true},
		{"all satisfies delete", PermAll, PermDelete, true},
		{"read does not satisfy insert", PermRead, PermInsert, false},
		{"read does not satisfy delete", PermRead, PermDelete, false},
		{"insert does not satisfy read", PermInsert, PermRead, false},
		{"write satisfies insert", PermWrite, PermInsert, true},
		{"write satisfies update", PermWrite, PermUpdate, true},
		{"write does not satisfy read", PermWrite, PermRead, false},
		{"write does not satisfy delete", PermWrite, PermDelete, false},
		{"zero satisfies nothing", 0, PermRead, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.Has(tt.required); got != tt.want {
				t.Errorf("Permission(%d).Has(%d) = %v, want %v", tt.p, tt.required, got, tt.want)
			}
		})
	}
}
