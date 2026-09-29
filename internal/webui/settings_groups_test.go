package webui

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Each Settings group is one form that sends only its own fields, so saving
// it can neither save nor reset what another group holds.
func TestEverySettingsGroupSendsOnlyItsOwnFields(t *testing.T) {
	r := newRenderer(t)
	own := map[string][]string{
		GroupUpdate:     {"update_check"},
		GroupAccess:     {"access_mode", "access_password"},
		GroupAdmin:      {"new_admin_password"},
		GroupConfirm:    {"admin_confirmation", "no_ask_ack"},
		GroupSession:    {"general_session"},
		GroupConnection: {"insecure_ack"},
		GroupNetwork:    {"network_revision", "listen", "base_url", "allowed_hosts", "trusted_proxies", "insecure_ack"},
		GroupTailscale:  {"tailscale", "home_network"},
	}
	groupPattern := regexp.MustCompile(`<section class="grp" id="grp-([a-z]+)"`)
	namePattern := regexp.MustCompile(`name="([a-z_]+)"`)
	seen := map[string]bool{}
	for _, name := range []string{"settings", "settings-access", "settings-network"} {
		c := fullChrome(LangEN)
		c.Connection = Connection{Encrypted: false, Host: "owngit.local:8080"}
		page := allPages(LangEN)[name].(SettingsPage)
		page.Chrome = c
		out := render(t, r, page)
		for _, m := range groupPattern.FindAllStringSubmatchIndex(out, -1) {
			group := out[m[2]:m[3]]
			section := out[m[0]:]
			section = section[:strings.Index(section, "</section>")]
			// The display choices of this browser are not a group: they
			// apply at once and are never posted.
			start := strings.Index(section, "<form")
			if start < 0 || !strings.Contains(section, `data-group="`+group+`"`) {
				continue
			}
			form := section[start : start+strings.Index(section[start:], "</form>")]
			seen[group] = true
			if !strings.Contains(form, `method="post" action="`+SettingsTabURL(SettingsGroupTab(group))+`"`) || !strings.Contains(form, "data-group-form") {
				t.Errorf("%s: the form is not a group form posted to its own tab", group)
			}
			for _, field := range namePattern.FindAllStringSubmatch(form, -1) {
				if name := field[1]; name != "csrf" && name != "action" && name != "admin_password" && !slices.Contains(own[group], name) {
					t.Errorf("%s: the form sends %q, which is not its own", group, name)
				}
			}
			// This browser has not typed the administrator password, so
			// every group asks for it and the browser, not the script,
			// sends each of them.
			if !strings.Contains(form, `type="password"`) {
				t.Errorf("%s: the form has no password field", group)
			}
		}
	}
	for group := range own {
		if !seen[group] {
			t.Errorf("no page showed the %s group", group)
		}
	}
}

// groupForms returns the form of each Settings group on a rendered tab.
func groupForms(out string) map[string]string {
	forms := map[string]string{}
	for _, m := range regexp.MustCompile(`<section class="grp" id="grp-([a-z]+)"`).FindAllStringSubmatchIndex(out, -1) {
		section := out[m[0]:]
		section = section[:strings.Index(section, "</section>")]
		start := strings.Index(section, "<form")
		if start < 0 || !strings.Contains(section, "data-group-form") {
			continue
		}
		forms[out[m[2]:m[3]]] = section[start : start+strings.Index(section[start:], "</form>")]
	}
	return forms
}

// While a change needs no password, in a remembered browser or with the
// check off, a group has no password field, so the script saves it and the
// other groups keep what was typed in them. Changing the administrator
// password still asks for the current one, and turning the check off asks
// one last time, only while that choice is picked.
func TestSettingsGroupsAskForThePasswordOnlyWhenAChangeNeedsIt(t *testing.T) {
	r := newRenderer(t)
	for _, check := range []struct {
		name   string
		viewer func(*Viewer)
		note   MessageCode
	}{
		{"remembered", func(v *Viewer) { v.AdminConfirmed, v.AdminRemembered, v.AdminAsks = true, true, false }, MsgConfirmRemembered},
		{"check off", func(v *Viewer) {
			v.AdminConfirmed, v.AdminCheckOff, v.AdminChoice, v.AdminAsks = true, true, "never", false
		}, MsgConfirmCheckOff},
	} {
		for _, name := range []string{"settings", "settings-access", "settings-network"} {
			c := fullChrome(LangEN)
			check.viewer(&c.Viewer)
			page := allPages(LangEN)[name].(SettingsPage)
			page.Chrome = c
			out := render(t, r, page)
			for group, form := range groupForms(out) {
				switch group {
				case GroupAdmin:
					if !strings.Contains(form, `name="admin_password"`) {
						t.Errorf("%s: changing the administrator password does not ask for the current one", check.name)
					}
				case GroupConfirm:
					asks := strings.Contains(form, `data-show-if="admin_confirmation=never"`) && strings.Contains(form, `name="admin_password"`)
					if asks != (check.name == "remembered") {
						t.Errorf("%s: the confirmation group asks for the password one last time: %v", check.name, asks)
					}
				default:
					if strings.Contains(form, `type="password"`) && group != GroupAccess {
						t.Errorf("%s: the %s group asks for a password its change does not need", check.name, group)
					}
					if strings.Contains(form, `name="admin_password"`) {
						t.Errorf("%s: the %s group asks for the administrator password", check.name, group)
					}
					if !strings.Contains(form, wantText(LangEN, check.note)) {
						t.Errorf("%s: the %s group does not say why it saves without the password", check.name, group)
					}
				}
			}
			if hasPill := strings.Contains(out, "data-admin-check-off"); hasPill != (check.name == "check off") {
				t.Errorf("%s: the page shows the check-off indication: %v", check.name, hasPill)
			}
		}
	}
}

// Every Settings tab carries the leave dialog, closed. Its fields belong to
// no form, so without the script they are never sent; the script attaches
// them to one group's form only for Save and leave.
func TestTheLeaveDialogBelongsToNoForm(t *testing.T) {
	r := newRenderer(t)
	for _, name := range []string{"settings", "settings-access", "settings-network"} {
		page := allPages(LangKO)[name].(SettingsPage)
		out := render(t, r, page)
		start := strings.Index(out, `<dialog class="leave"`)
		if start < 0 {
			t.Fatalf("%s: no leave dialog", name)
		}
		dialog := out[start : start+strings.Index(out[start:], "</dialog>")]
		opening := dialog[:strings.Index(dialog, ">")]
		if strings.Contains(opening, " open") {
			t.Errorf("%s: the dialog starts open", name)
		}
		if strings.Contains(dialog, "<form") || strings.Contains(dialog, " form=") {
			t.Errorf("%s: a field of the dialog belongs to a form", name)
		}
		if forms := strings.Count(out[:start], "<form") - strings.Count(out[:start], "</form>"); forms != 0 {
			t.Errorf("%s: the dialog sits inside a form", name)
		}
		for _, want := range []string{`name="admin_password" type="password"`, `name="leave_to"`, "data-leave-save", "data-leave-discard", "data-leave-stay",
			Text(LangKO, MsgLeaveSave), Text(LangKO, MsgLeaveDiscard), Text(LangKO, MsgLeaveStay), Text(LangEN, MsgLeaveStay)} {
			if !strings.Contains(dialog, want) {
				t.Errorf("%s: the dialog lacks %q", name, want)
			}
		}
	}
}
