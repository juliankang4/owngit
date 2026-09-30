package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// Save and enable first shows what would change and changes nothing; the
// confirmation then saves exactly the reviewed policy and turns checks on for
// it, and only while the stored policy is still the one the review compared
// against.
func TestBrowserSaveAndEnableConfirmsTheReviewedChange(t *testing.T) {
	fixture := newAPIFixture(t, false)
	askEveryTime(t, fixture.app)
	server, client, jar := openBrowser(t, fixture)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "cc-save-enable")
	policyURL := server.URL + configuredChecksURL("project")

	saved := browserForm(t, client, policyURL, validPolicyValues(csrf), server.URL)
	if saved.status != http.StatusSeeOther {
		t.Fatalf("first save status=%d", saved.status)
	}
	stored, _, err := fixture.store.CheckPolicy(context.Background(), "project")
	noErr(t, err)

	changed := validPolicyValues(csrf)
	changed.Set("action", webui.ActionReviewSaveAndEnable)
	changed.Del("admin_password")
	changed.Set("max_timeout_ms", "300000")
	review := browserForm(t, client, policyURL, changed, server.URL)
	if review.status != http.StatusOK || !strings.Contains(review.body, browserText(webui.MsgCCReviewWarn)) ||
		!strings.Contains(review.body, `id="cc-review"`) || !strings.Contains(review.body, "10 minutes") || !strings.Contains(review.body, "5 minutes") {
		t.Fatalf("review status=%d body lacks the change or its warning", review.status)
	}
	if current, _, err := fixture.store.CheckPolicy(context.Background(), "project"); err != nil || current.Digest != stored.Digest || current.ConsentActive {
		t.Fatalf("the review changed the stored policy: %+v err=%v", current, err)
	}
	confirm := validPolicyValues(csrf)
	confirm.Set("action", webui.ActionSaveAndEnable)
	confirm.Set("max_timeout_ms", "300000")
	for _, name := range []string{"review_digest", "base_version", "base_digest"} {
		confirm.Set(name, formValue(t, review.body, name))
	}

	// A form edited after the review is reviewed again, not saved.
	edited := cloneValues(confirm)
	edited.Set("queue_limit", "21")
	if result := browserForm(t, client, policyURL, edited, server.URL); result.status != http.StatusConflict || !strings.Contains(result.body, browserText(webui.MsgCCReviewChanged)) {
		t.Fatalf("edited after review status=%d", result.status)
	}

	// Another save between review and confirmation refuses the step.
	other := validPolicyValues(csrf)
	other.Set("queue_limit", "30")
	if result := browserForm(t, client, policyURL, other, server.URL); result.status != http.StatusSeeOther {
		t.Fatalf("concurrent save status=%d", result.status)
	}
	if result := browserForm(t, client, policyURL, confirm, server.URL); result.status != http.StatusConflict || !strings.Contains(result.body, browserText(webui.MsgCCPolicyStale)) {
		t.Fatalf("confirmation over another change status=%d", result.status)
	}
	current, _, err := fixture.store.CheckPolicy(context.Background(), "project")
	noErr(t, err)
	if current.ConsentActive || current.QueueLimit != 30 {
		t.Fatalf("a stale confirmation changed the policy or consent: %+v", current)
	}

	// Reviewed again against the current policy, the confirmation needs the
	// password and then saves and enables exactly the reviewed policy.
	review = browserForm(t, client, policyURL, changed, server.URL)
	for _, name := range []string{"review_digest", "base_version", "base_digest"} {
		confirm.Set(name, formValue(t, review.body, name))
	}
	noPassword := cloneValues(confirm)
	noPassword.Del("admin_password")
	if result := browserForm(t, client, policyURL, noPassword, server.URL); result.status != http.StatusUnauthorized || !strings.Contains(result.body, browserText(webui.MsgCCReviewWarn)) {
		t.Fatalf("confirmation without a password status=%d", result.status)
	}
	done := browserForm(t, client, policyURL, confirm, server.URL)
	if done.status != http.StatusSeeOther || done.header.Get("Location") != configuredChecksURL("project")+"?notice=check_policy_saved_turned_on" {
		t.Fatalf("confirmation status=%d location=%q", done.status, done.header.Get("Location"))
	}
	enabled, _, err := fixture.store.CheckPolicy(context.Background(), "project")
	noErr(t, err)
	if !enabled.ConsentActive || enabled.ConsentDigest != confirm.Get("review_digest") || enabled.MaxTimeoutMS != 300000 || enabled.QueueLimit != 20 {
		t.Fatalf("save and enable stored %+v", enabled)
	}
}

// The container options travel from the form to the stored policy and back,
// and a named network needs its name.
func TestBrowserContainerOptionsAreSavedAndShown(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "cc-options")
	policyURL := server.URL + configuredChecksURL("project")

	values := validPolicyValues(csrf)
	values.Set("executor", webui.ExecutorContainer)
	values.Set("container_image", "registry.example:5000/team/checks:1")
	values.Set("container_allow_tags", "1")
	values.Set("container_pull_missing", "1")
	values.Set("container_image_volumes", "1")
	values.Set("container_writable_root", "1")
	values["container_missing_enforcement"] = []string{"swap", "cpu"}
	values.Set("container_network", webui.ContainerNetworkNamed)

	if result := browserForm(t, client, policyURL, values, server.URL); result.status != http.StatusUnprocessableEntity || !strings.Contains(result.body, browserText(webui.MsgCCNetworkInvalid)) {
		t.Fatalf("named network without a name status=%d", result.status)
	}
	values.Set("container_network_name", "host")
	if result := browserForm(t, client, policyURL, values, server.URL); result.status != http.StatusUnprocessableEntity || !strings.Contains(result.body, browserText(webui.MsgCCNetworkHost)) {
		t.Fatalf("host network status=%d", result.status)
	}
	values.Set("container_network_name", "checks-net")
	if result := browserForm(t, client, policyURL, values, server.URL); result.status != http.StatusSeeOther {
		t.Fatalf("container options save status=%d body=%s", result.status, noticeRegion(t, result.body))
	}
	stored, _, err := fixture.store.CheckPolicy(context.Background(), "project")
	noErr(t, err)
	execution := stored.Execution
	if !execution.ContainerAllowTags || !execution.ContainerPullMissing || !execution.ContainerImageVolumes || !execution.ContainerWritableRoot ||
		strings.Join(execution.ContainerMissingEnforcement, ",") != "cpu,swap" || execution.ContainerNetwork != "checks-net" {
		t.Fatalf("stored container options %+v", execution)
	}

	page := browserGET(t, client, policyURL)
	for _, want := range []string{
		`name="container_allow_tags" value="1" checked`, `name="container_pull_missing" value="1" checked`,
		`name="container_image_volumes" value="1" checked`, `name="container_writable_root" value="1" checked`,
		`name="container_missing_enforcement" value="swap" checked`, `name="container_missing_enforcement" value="cpu" checked`,
		`name="container_network" value="named" checked`, `value="checks-net"`, "registry.example:5000",
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("the saved options are not shown: missing %s", want)
		}
	}
	if strings.Contains(page.body, `name="container_missing_enforcement" value="memory" checked`) {
		t.Error("an unaccepted limit is shown as accepted")
	}

	// Leaving the container mode refuses nothing: the options belong to it
	// and are simply not sent.
	host := validPolicyValues(csrf)
	host.Set("container_allow_tags", "1")
	if result := browserForm(t, client, policyURL, host, server.URL); result.status != http.StatusSeeOther {
		t.Fatalf("host policy with a hidden container option status=%d", result.status)
	}
	if stored, _, err := fixture.store.CheckPolicy(context.Background(), "project"); err != nil || stored.Executor != state.CheckExecutorHost || stored.Execution.HasContainerOptions() {
		t.Fatalf("host policy stored %+v err=%v", stored, err)
	}
}

func cloneValues(values url.Values) url.Values {
	clone := url.Values{}
	for key, value := range values {
		clone[key] = append([]string(nil), value...)
	}
	return clone
}
