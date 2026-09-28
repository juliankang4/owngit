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
			// Today every group asks for the administrator password, so the
			// browser, not the script, sends each of them.
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
