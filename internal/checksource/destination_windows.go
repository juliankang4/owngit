//go:build windows

package checksource

import (
	"os"

	"owngit/internal/state"
)

// protectDestination applies and verifies an inheritable owner-only ACL on the
// new destination directory.
//
// Windows ignores the Unix mode bits passed to os.Mkdir, so a newly created
// directory inherits its parent's access entries. A permissive parent would
// otherwise leave the materialized source readable by other users. The ACL is
// therefore established here, before any content is written.
//
// state.ProtectPrivatePath owns the ACL policy: it confirms the object is
// owned by the current process user or its token owner, replaces the DACL with
// a protected owner-only grant that inherits to both files and subdirectories,
// and then reads the descriptor back to verify the result. Failure propagates,
// so the export stops instead of continuing into an unprotected directory.
//
// The path form is used rather than the held handle because os.OpenRoot opens
// its directory without WRITE_DAC, which SetSecurityInfo requires. The window
// between creation and protection is covered by the trusted-parent contract in
// destination.go and by the emptiness check that follows this call.
func protectDestination(_ *os.Root, destination string) error {
	return state.ProtectPrivatePath(destination, true)
}
