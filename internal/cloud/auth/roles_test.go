package auth

import "testing"

func TestRank(t *testing.T) {
	cases := []struct {
		role string
		want int
	}{
		{RoleOwner, 3},
		{RoleAdmin, 2},
		{RoleModerator, 1},
		{RoleMember, 0},
		{"", rankUnknown},
		{"superuser", rankUnknown},
		{"OWNER", rankUnknown}, // case-sensitive: only exact lowercase matches
	}
	for _, c := range cases {
		if got := rank(c.role); got != c.want {
			t.Errorf("rank(%q) = %d, want %d", c.role, got, c.want)
		}
	}
}

func TestRankStrictOrdering(t *testing.T) {
	if !(rank(RoleOwner) > rank(RoleAdmin) &&
		rank(RoleAdmin) > rank(RoleModerator) &&
		rank(RoleModerator) > rank(RoleMember) &&
		rank(RoleMember) > rankUnknown) {
		t.Fatalf("role ranks are not in strict descending order owner>admin>moderator>member>unknown")
	}
}

func TestCanManageMembers(t *testing.T) {
	cases := []struct {
		role string
		want bool
	}{
		{RoleOwner, true},
		{RoleAdmin, true},
		{RoleModerator, true},
		{RoleMember, false},
		{"", false},
		{"unknown", false},
	}
	for _, c := range cases {
		if got := CanManageMembers(c.role); got != c.want {
			t.Errorf("CanManageMembers(%q) = %v, want %v", c.role, got, c.want)
		}
	}
}

func TestCanActorAssign(t *testing.T) {
	cases := []struct {
		name      string
		actor     string
		target    string
		want      bool
		rationale string
	}{
		// Owner: may assign admin and below, but NOT owner.
		{"owner assigns admin", RoleOwner, RoleAdmin, true, "owner exception allows admin"},
		{"owner assigns moderator", RoleOwner, RoleModerator, true, ""},
		{"owner assigns member", RoleOwner, RoleMember, true, ""},
		{"owner assigns owner BLOCKED", RoleOwner, RoleOwner, false, "ownership not transferable"},

		// Admin: may assign moderator and member, but NOT admin (self-level) or owner.
		{"admin assigns moderator", RoleAdmin, RoleModerator, true, ""},
		{"admin assigns member", RoleAdmin, RoleMember, true, ""},
		{"admin assigns admin BLOCKED (self-level escalation)", RoleAdmin, RoleAdmin, false, "no self-level assign"},
		{"admin assigns owner BLOCKED (escalation)", RoleAdmin, RoleOwner, false, "no upward assign"},

		// Moderator: may assign ONLY member.
		{"moderator assigns member", RoleModerator, RoleMember, true, ""},
		{"moderator assigns moderator BLOCKED (self-level)", RoleModerator, RoleModerator, false, "no self-level assign"},
		{"moderator assigns admin BLOCKED (escalation)", RoleModerator, RoleAdmin, false, "core escalation block"},
		{"moderator assigns owner BLOCKED (escalation)", RoleModerator, RoleOwner, false, "core escalation block"},

		// Member: may assign nothing.
		{"member assigns member BLOCKED", RoleMember, RoleMember, false, "member cannot manage"},
		{"member assigns admin BLOCKED", RoleMember, RoleAdmin, false, "member cannot manage"},

		// Unknown actor / target.
		{"unknown actor BLOCKED", "ghost", RoleMember, false, "unknown actor cannot manage"},
		{"any actor assigns unknown role BLOCKED", RoleOwner, "wizard", false, "unknown target rejected"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CanActorAssign(c.actor, c.target); got != c.want {
				t.Errorf("CanActorAssign(%q, %q) = %v, want %v (%s)", c.actor, c.target, got, c.want, c.rationale)
			}
		})
	}
}

func TestCanActorModifyTarget(t *testing.T) {
	cases := []struct {
		name      string
		actor     string
		target    string
		want      bool
		rationale string
	}{
		// Owner may modify everyone except the owner (no self-removal here, no peer owner).
		{"owner modifies admin", RoleOwner, RoleAdmin, true, ""},
		{"owner modifies moderator", RoleOwner, RoleModerator, true, ""},
		{"owner modifies member", RoleOwner, RoleMember, true, ""},
		{"owner modifies owner BLOCKED", RoleOwner, RoleOwner, false, "owner never removable"},

		// Admin may modify moderator/member but NOT other admins or the owner.
		{"admin modifies moderator", RoleAdmin, RoleModerator, true, ""},
		{"admin modifies member", RoleAdmin, RoleMember, true, ""},
		{"admin modifies admin BLOCKED (peer)", RoleAdmin, RoleAdmin, false, "no peer modification"},
		{"admin modifies owner BLOCKED", RoleAdmin, RoleOwner, false, "owner never removable"},

		// Moderator may modify members only.
		{"moderator modifies member", RoleModerator, RoleMember, true, ""},
		{"moderator modifies moderator BLOCKED (peer)", RoleModerator, RoleModerator, false, "no peer modification"},
		{"moderator modifies admin BLOCKED", RoleModerator, RoleAdmin, false, "cannot touch higher"},
		{"moderator modifies owner BLOCKED", RoleModerator, RoleOwner, false, "owner never removable"},

		// Member may modify nobody.
		{"member modifies member BLOCKED", RoleMember, RoleMember, false, "member cannot manage"},
		{"member modifies owner BLOCKED", RoleMember, RoleOwner, false, "member cannot manage"},

		// Unknown actor/target.
		{"unknown actor BLOCKED", "ghost", RoleMember, false, "unknown actor cannot manage"},
		{"owner modifies unknown target BLOCKED", RoleOwner, "wizard", false, "unknown target rejected"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CanActorModifyTarget(c.actor, c.target); got != c.want {
				t.Errorf("CanActorModifyTarget(%q, %q) = %v, want %v (%s)", c.actor, c.target, got, c.want, c.rationale)
			}
		})
	}
}
