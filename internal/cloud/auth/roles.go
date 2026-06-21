package auth

// Role constants for project membership. Roles form a strict total order:
//
//	owner (3) > admin (2) > moderator (1) > member (0)
//
// The numeric rank (see rank) is the single source of truth for all
// privilege comparisons. Higher rank means more authority.
const (
	RoleOwner     = "owner"
	RoleAdmin     = "admin"
	RoleModerator = "moderator"
	RoleMember    = "member"
)

// rankUnknown is the rank returned for any role string that is not one of the
// four known roles. It is strictly lower than every real role so that an
// unknown actor can never manage anyone and is never out-ranked by a real role
// in a way that would grant access.
const rankUnknown = -1

// rank maps a role string to its numeric privilege rank. Unknown roles map to
// rankUnknown (-1). The ordering is the authoritative privilege hierarchy used
// by every authorization helper below.
func rank(role string) int {
	switch role {
	case RoleOwner:
		return 3
	case RoleAdmin:
		return 2
	case RoleModerator:
		return 1
	case RoleMember:
		return 0
	default:
		return rankUnknown
	}
}

// CanManageMembers reports whether a role is allowed to manage members at all.
// Only moderator and above may manage members; members (and unknown roles)
// cannot. This is the coarse gate every management endpoint applies first.
func CanManageMembers(role string) bool {
	return rank(role) >= rank(RoleModerator)
}

// canActorAssign reports whether an actor with actorRole may assign targetRole
// to a member (on create or role change).
//
// Anti-escalation rules (ALL enforced here):
//   - The actor must be able to manage members at all.
//   - The target role must be a known role.
//   - GENERAL RULE: an actor may never assign a role >= the actor's own role,
//     with EXACTLY ONE exception: the owner may assign admin (and any lower
//     role). The owner cannot assign owner — ownership is not transferable via
//     these endpoints.
//   - moderator may assign only member.
//
// Concretely this yields:
//   - owner  -> may assign {admin, moderator, member}     (NOT owner)
//   - admin  -> may assign {moderator, member}            (NOT admin, owner)
//   - mod    -> may assign {member}                       (NOT moderator, admin, owner)
//   - member -> may assign nothing
func CanActorAssign(actorRole, targetRole string) bool {
	if !CanManageMembers(actorRole) {
		return false
	}
	actorRank := rank(actorRole)
	targetRank := rank(targetRole)
	// Reject unknown target roles outright — never assign an unrecognized role.
	if targetRank == rankUnknown {
		return false
	}
	// Nobody may ever assign owner through these endpoints. Ownership is
	// established only by claim-on-first-push, never granted to others.
	if targetRank == rank(RoleOwner) {
		return false
	}
	// Owner exception: the owner may assign admin (the highest assignable role)
	// and anything below it.
	if actorRank == rank(RoleOwner) {
		return targetRank <= rank(RoleAdmin)
	}
	// General rule for everyone else: may only assign roles strictly BELOW the
	// actor's own rank. This blocks self-level and upward assignment, which is
	// the core privilege-escalation defense.
	return targetRank < actorRank
}

// canActorModifyTarget reports whether an actor with actorRole may modify or
// remove a member who currently holds targetRole.
//
// Anti-escalation rules (ALL enforced here):
//   - The actor must be able to manage members at all.
//   - GENERAL RULE: an actor may never modify/remove a target whose role is >=
//     the actor's own role. This blocks admins from touching other admins or
//     the owner, moderators from touching admins/owner, and any peer-on-peer
//     modification.
//   - The owner can never be modified/removed via these endpoints (the rule
//     above already covers this for every non-owner actor, since owner has the
//     maximum rank; the owner acting on itself is additionally pointless and is
//     blocked by the strict ">=" comparison).
func CanActorModifyTarget(actorRole, targetRole string) bool {
	if !CanManageMembers(actorRole) {
		return false
	}
	actorRank := rank(actorRole)
	targetRank := rank(targetRole)
	// Unknown target role: refuse to act rather than risk an unsafe decision.
	if targetRank == rankUnknown {
		return false
	}
	// The owner is never removable/modifiable by anyone through these endpoints.
	if targetRank == rank(RoleOwner) {
		return false
	}
	// May act only on targets strictly below the actor's own rank.
	return targetRank < actorRank
}
