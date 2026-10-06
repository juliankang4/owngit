package importgit

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// validateRefName applies the ref naming rules of gitprotocol-common(5) and
// git-check-ref-format(1) to an advertised name.
//
// Only "HEAD" and names rooted at "refs/" are accepted, which is what the
// protocol grammar defines. Every other namespace under "refs/" is allowed,
// including ones this project does not itself create, because rejecting an
// unfamiliar namespace would lose a fact the caller needs. Accepting a name
// here is not permission to publish it.
//
// The name is validated as bytes and never rewritten. A name that cannot be
// represented losslessly is refused rather than normalized, so no advertised
// ref silently becomes a different ref.
func validateRefName(name string) error {
	if name == "HEAD" {
		return nil
	}
	if !strings.HasPrefix(name, "refs/") {
		return errors.New("the name is neither HEAD nor rooted at refs/")
	}
	// Advertised names are octet strings, but a name that is not valid UTF-8
	// cannot be displayed, stored in this project's state, or compared
	// reliably. Refusing it is honest; re-encoding it would change the ref.
	if !utf8.ValidString(name) {
		return errors.New("the name is not valid UTF-8, so it cannot be represented without loss")
	}
	return CheckRefFormat(name)
}

// ValidBranchName reports whether "refs/heads/"+name is a legal ref name that
// is also valid UTF-8. Callers add their own length and namespace rules.
func ValidBranchName(name string) bool {
	return utf8.ValidString(name) && CheckRefFormat("refs/heads/"+name) == nil
}

// CheckRefFormat applies the rules of "git check-ref-format <name>" (no
// options) to a full ref name: at least two components, no empty component,
// no component that starts with "." or ends with ".lock", none of the
// forbidden characters or sequences, no trailing "." or "/", and not "@".
// A dot at the end of a component other than the last is legal ("a./b").
// It checks bytes only and does not require valid UTF-8.
func CheckRefFormat(name string) error {
	if name == "@" {
		return errors.New("the name is a single @")
	}
	if strings.HasSuffix(name, ".") {
		return errors.New("the name ends with a dot")
	}
	if strings.Contains(name, "..") {
		return errors.New("the name contains two consecutive dots")
	}
	if strings.Contains(name, "@{") {
		return errors.New("the name contains the reflog sequence @{")
	}
	for index := 0; index < len(name); index++ {
		if name[index] < 0x20 || name[index] == 0x7f {
			return errors.New("the name contains an ASCII control character")
		}
	}
	if strings.ContainsAny(name, "\\~^: ?*[") {
		return errors.New("the name contains a character Git forbids in a ref")
	}
	components := strings.Split(name, "/")
	if len(components) < 2 {
		return errors.New("the name has only one component")
	}
	for _, component := range components {
		if component == "" {
			return errors.New("the name has an empty or repeated path component")
		}
		if strings.HasPrefix(component, ".") {
			return errors.New("a name component begins with a dot")
		}
		if strings.HasSuffix(component, ".lock") {
			return errors.New("a name component ends with .lock")
		}
	}
	return nil
}
