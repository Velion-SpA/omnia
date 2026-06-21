package auth

import "errors"

// Permission is a bitfield of access rights for a project.
type Permission int

const (
	PermRead   Permission = 1
	PermInsert Permission = 2
	PermUpdate Permission = 4
	PermDelete Permission = 8
	PermWrite             = PermInsert | PermUpdate
	PermAll               = PermRead | PermWrite | PermDelete
)

// Has reports whether p includes all the bits in required.
func (p Permission) Has(required Permission) bool {
	return p&required == required
}

// ErrPermissionDenied is returned when an account lacks the required permission
// for the requested project operation.
var ErrPermissionDenied = errors.New("auth: permission denied")
