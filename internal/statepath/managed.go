package statepath

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

const (
	Database          = "owngit.sqlite"
	WALSuffix         = "-wal"
	SHMSuffix         = "-shm"
	JournalSuffix     = "-journal"
	IncompleteRestore = ".owngit-restore-pending"
	HealthRun         = "health-run.json"
	TrayAccess        = "tray-access.json"
	TrayHidden        = "tray-hidden"
	TrayNotifications = "tray-notifications.json"
	TrayCursor        = "tray-cursor"
	OfflineLock       = ".offline-operation.lock"
	RunningLock       = ".network-running.lock"
	TailscaleLock     = ".tailscale-change.lock"
	DatabaseLock      = ".database-create.lock"
	UpgradeBackupOff  = "no-upgrade-backup"
	SetupPage         = "owner-setup.html"
	SetupLock         = ".owner-setup.lock"
	SetupJournal      = ".owner-setup.issue.json"
	SetupTemporary    = ".owner-setup-*"
	JournalTemporary  = ".owner-setup-journal-*"
	ServeError        = "serve-error.txt"
	Runtime           = "runtime"
	GitHome           = "git-home"
	Temporary         = "tmp"
	GitConfig         = "gitconfig.empty"
	ImportCredentials = "import-credentials"
	WorkflowSecrets   = "workflow-secrets"
	CredentialSuffix  = ".json"
	CredentialWrite   = ".tmp-"
	CredentialRestore = ".restore-"
	Logs              = "logs"
	ServiceLog        = "service.log"
	PreviousLog       = ServiceLog + ".1"
)

var managedPaths = [...]string{
	Database, Database + WALSuffix, Database + SHMSuffix,
	HealthRun, TrayAccess, TrayHidden, TrayNotifications, TrayCursor,
	OfflineLock, RunningLock, TailscaleLock, DatabaseLock, UpgradeBackupOff,
	SetupPage, SetupLock, SetupJournal, ServeError,
	Runtime, Runtime + "/" + GitHome, Runtime + "/" + Temporary, Runtime + "/" + GitConfig,
	ImportCredentials, WorkflowSecrets, Logs, Logs + "/" + ServiceLog, Logs + "/" + PreviousLog,
}

// Managed selects only OwnGit's entries, never repository or workspace contents.
func Managed(parent, name string) bool {
	path := name
	if parent != "" {
		path = parent + "/" + name
	}
	for _, managed := range managedPaths {
		if path == managed {
			return true
		}
	}
	if parent == "" {
		if suffix, ok := strings.CutPrefix(name, Database+".new-"); ok {
			return encodedSuffix(suffix, hex.DecodeString)
		}
		for _, file := range []string{HealthRun, TrayAccess, TrayNotifications, TrayCursor} {
			if suffix, ok := strings.CutPrefix(name, "."+file+"-"); ok {
				return encodedSuffix(suffix, base64.RawURLEncoding.DecodeString)
			}
		}
		for _, pattern := range []string{SetupTemporary, JournalTemporary} {
			if suffix, ok := strings.CutPrefix(name, strings.TrimSuffix(pattern, "*")); ok && suffix != "" && strings.Trim(suffix, "0123456789") == "" {
				return true
			}
		}
	}
	if parent == ImportCredentials || parent == WorkflowSecrets {
		if id, ok := strings.CutSuffix(name, CredentialSuffix); ok {
			return credentialID(id)
		}
		_, temporary := CredentialTemporaryID(name)
		return temporary
	}
	return false
}

func DatabaseFile(name string) bool {
	return name == Database || name == Database+WALSuffix || name == Database+SHMSuffix
}

func HasChildren(path string) bool {
	if path == ImportCredentials || path == WorkflowSecrets {
		return true
	}
	for _, managed := range managedPaths {
		if strings.HasPrefix(managed, path+"/") {
			return true
		}
	}
	return false
}

func ReplacementTemporary(name string, random []byte) string {
	return "." + name + "-" + base64.RawURLEncoding.EncodeToString(random)
}

func DatabaseTemporary(random []byte) string {
	return Database + ".new-" + hex.EncodeToString(random)
}

func CredentialTemporary(id, operation string, random []byte) string {
	return "." + id + operation + hex.EncodeToString(random)
}

// CredentialTemporaryID returns the repository ID of a private temporary file.
func CredentialTemporaryID(name string) (string, bool) {
	name, ok := strings.CutPrefix(name, ".")
	if !ok {
		return "", false
	}
	for _, separator := range []string{CredentialWrite, CredentialRestore} {
		cut := strings.LastIndex(name, separator)
		if cut >= 0 && credentialID(name[:cut]) && encodedSuffix(name[cut+len(separator):], hex.DecodeString) {
			return name[:cut], true
		}
	}
	return "", false
}

func encodedSuffix(suffix string, decode func(string) ([]byte, error)) bool {
	decoded, err := decode(suffix)
	return err == nil && len(decoded) == 8
}

func credentialID(id string) bool {
	id = strings.TrimSpace(id)
	return id != "" && len(id) <= 100 && utf8.ValidString(id) && !strings.ContainsAny(id, "\x00\r\n")
}
