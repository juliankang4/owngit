package webui

import "time"

// Share links. The administrator creates and revokes them on a repository's
// Share links screen; a visitor who holds one reads the repository's code
// pages (RepositoryPage with Shared set) and, for a password link, first
// types its password on SharePasswordPage.

// Actions of the Share links screen's forms.
const (
	ActionCreateShareLink = "create_share_link"
	ActionRevokeShareLink = "revoke_share_link"
)

// ShareExpiryChoices are the expiries the create form offers, in days;
// ShareExpiryNever keeps a link until it is revoked, and
// ShareExpiryDefault is preselected.
var ShareExpiryChoices = []string{"1", "7", "30", "90", ShareExpiryNever}

const (
	ShareExpiryNever   = "never"
	ShareExpiryDefault = "30"
)

// ShareLinksPage renders /repositories/{id}/share-links.
type ShareLinksPage struct {
	Chrome Chrome
	Repo   RepositoryHeader
	Tabs   RepoTabs
	// SubmitURL is the POST target of the create and revoke forms, and the
	// screen's own GET address.
	SubmitURL string
	// Links are the repository's links, revoked and expired ones included.
	Links []ShareLinkRow
	// Created is the link this response created. Its address exists in
	// this one response: OwnGit keeps only the SHA-256 of its secret.
	Created *CreatedShareLink
	// Form is what a refused creation sent, without its password, and the
	// defaults otherwise. FormNotices are its notices.
	Form        ShareLinkForm
	FormNotices []Notice
	// RevokeID names the row whose revoke was refused, and RevokeNotices
	// are that refusal's notices.
	RevokeID      string
	RevokeNotices []Notice
}

// NoticesFor returns the notices of the row of link id: those of a refused
// revoke of that link, if any.
func (p ShareLinksPage) NoticesFor(id string) []Notice {
	if id != p.RevokeID {
		return nil
	}
	return p.RevokeNotices
}

func (ShareLinksPage) page() string     { return "share-links" }
func (p ShareLinksPage) chrome() Chrome { return p.Chrome }

// ShareLinkForm is the create form's values.
type ShareLinkForm struct {
	Label  string
	Scope  string
	Expiry string
}

// ShareLinkRow is one link in the list. A zero time means none.
type ShareLinkRow struct {
	ID string
	// ShortID is the start of the ID. A visitor's pages are under
	// /share/{ID}/, so it tells which link an address belongs to.
	ShortID     string
	Label       string
	Scope       string
	HasPassword bool
	CreatedAt   time.Time
	ExpiresAt   time.Time
	RevokedAt   time.Time
	LastUsedAt  time.Time
	// State is "active", "expired" or "revoked".
	State string
}

// CreatedShareLink is the link just created, with the addresses to hand
// over.
type CreatedShareLink struct {
	Label string
	// URL opens the repository in a browser; CloneURL, for a clone link,
	// is the Git address. CloneHelp says how Git signs in.
	URL       string
	CloneURL  string
	CloneHelp MessageCode
	// PublicURL and PublicCloneURL are the same on the public share
	// address, when the running server has one.
	PublicURL, PublicCloneURL string
	// Warnings say what the chosen expiry, scope and password allow.
	Warnings []MessageCode
}

// SharePasswordPage asks a visitor for the extra password of a share link.
// It names no repository until the password is right.
type SharePasswordPage struct {
	Chrome Chrome
	// SubmitURL is the address the visitor asked for; a right password
	// returns there.
	SubmitURL string
	// Wrong is true after a wrong password, and Locked while this address
	// may not try again, until RetryAfter.
	Wrong      bool
	Locked     bool
	RetryAfter time.Time
}

func (SharePasswordPage) page() string     { return "share-password" }
func (p SharePasswordPage) chrome() Chrome { return p.Chrome }
