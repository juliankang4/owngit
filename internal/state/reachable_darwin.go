//go:build darwin

package state

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

const kauthSearchRights = 1 << 3

func groupOrAccessListAllowsOtherSearch(file *os.File, info os.FileInfo) (bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, errors.New("owning group is unavailable")
	}
	if info.Mode().Perm()&0o010 != 0 && !OwnPrivateGroup(stat.Gid) && !rootEquivalentGroup(stat.Gid) {
		return true, nil
	}
	filesec, err := extendedSecurity(file.Name(), file, 0)
	if err != nil {
		return false, err
	}
	identities, err := matchingPermitIdentities(filesec, kauthSearchRights, nil)
	if err != nil {
		return false, err
	}
	for _, identity := range identities {
		output, err := runMembershipCommand("getid", "-X", accessListIdentityString(identity))
		kind, idText, found := strings.Cut(strings.TrimSpace(string(output)), ":")
		id, parseErr := strconv.ParseUint(strings.TrimSpace(idText), 10, 32)
		if err != nil || !found || parseErr != nil {
			return true, nil
		}
		switch kind {
		case "uid":
			if id != 0 && int(id) != os.Geteuid() {
				return true, nil
			}
		case "gid":
			gid := uint32(id)
			if !OwnPrivateGroup(gid) && !rootEquivalentGroup(gid) {
				return true, nil
			}
		default:
			return true, nil
		}
	}
	return false, nil
}
