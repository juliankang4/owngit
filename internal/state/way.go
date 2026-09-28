package state

// FolderWay describes the way to a folder that root might create for
// another account with a command that OwnGit suggests.
type FolderWay struct {
	// OnlyRoot says that root is the only account that can create, rename
	// or replace anything on the way: "/" and every existing folder and link
	// on the way belong to root, no other account can write to one of them,
	// not even to a sticky folder (group write by the macOS administrator
	// groups counts as root's, see rootEquivalentGroup), and no folder on
	// the way is Shared. Only then can a folder that root creates there not
	// be redirected by a link that another account put there first. Names
	// that do not exist yet are fine, because root creates them.
	OnlyRoot bool
	// Shared says that a folder on the way is on a filesystem whose owners
	// and modes this computer does not enforce (ownershipEnforced), such as
	// a network share, whose server can let others change what it shows as
	// root's. A link belongs to the folder that holds it, so a link on a
	// share that leads to a local folder makes the way Shared too.
	Shared bool
}

// createAttempts bounds how often OpenOwnFile tries to create a name that
// others keep creating and removing.
const createAttempts = 5
