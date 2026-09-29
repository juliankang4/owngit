package webui

import (
	"strings"
	"testing"
)

func TestSetupFolderChooserPreservesManualSubmission(t *testing.T) {
	for _, lang := range Langs() {
		page := SetupPage{Chrome: bareChrome(lang), Stage: SetupWizard, SubmitURL: "/setup", Form: SetupForm{StoragePath: "/synthetic/repositories", SuggestedPath: "/synthetic/suggestion"}}
		out := render(t, newRenderer(t), page)
		for _, part := range []string{
			`method="post" action="/setup"`, `name="storage_path" type="text"`, `value="/synthetic/repositories"`,
			`data-folder-open aria-haspopup="dialog" aria-controls="folder-chooser" hidden`,
			`<dialog id="folder-chooser"`, `aria-labelledby="folder-title" aria-describedby="folder-host"`,
			`data-list-url="/setup/folders" data-create-url="/setup/folders/new"`, `role="status" aria-live="polite"`,
			`data-folder-list aria-label=`, `data-folder-cancel autofocus`, `data-folder-use disabled`,
			`class="form f folderchooser__new"`,
			`data-folder-message="folder.denied" hidden`, `data-folder-message="folder.empty" hidden`,
			`data-en="Choose folder" data-ko="폴더 선택"`,
		} {
			if !strings.Contains(out, part) {
				t.Errorf("%s missing %q", lang, part)
			}
		}
		formEnd := strings.Index(out, "</form>")
		if formEnd < 0 || strings.Index(out, "<dialog") < formEnd {
			t.Error("dialog is nested inside setup submission")
		}
		page.Stage = SetupWelcome
		if strings.Contains(render(t, newRenderer(t), page), "data-folder-chooser") {
			t.Error("chooser exposed before owner session")
		}
	}
}

func TestFolderChooserRequestsOnlySetupEndpoints(t *testing.T) {
	block := section(t, scriptSource(t), "(function folderChooser()", "})();")
	for _, required := range []string{
		"target.origin !== window.location.origin", "target.pathname !== expected", "target.search || target.hash",
		"'/setup/folders/new' : '/setup/folders'", "method: 'POST'", "credentials: 'same-origin'", "redirect: 'manual'",
		"body.set('csrf', token.value)", "body.set('path', values.path)", "body.set('name', values.name)",
		"label.textContent = folder.name", "field.value = current", "dialog.showModal()", "opener.focus()",
		"ready = false", "say(result.error)", "controller.abort()", "list.replaceChildren()",
	} {
		if !strings.Contains(block, required) {
			t.Errorf("folder chooser missing %q", required)
		}
	}
	close := section(t, block, "dialog.addEventListener('close'", "dialog.querySelector('[data-folder-cancel]')")
	if guard, cancel := strings.Index(close, "if (dialog.open) { return; }"), strings.Index(close, "generation++"); guard < 0 || cancel < 0 || guard > cancel {
		t.Error("a queued close event can cancel a reopened dialog's request")
	}
	if strings.Count(block, "fetch(") != 1 {
		t.Error("chooser has more than one transport boundary")
	}
	for _, forbidden := range []string{"new FormData", "password", "Storage", "document.cookie", "console.", "location.hash", "innerHTML", "eval(", "http:", "https:", "://"} {
		if strings.Contains(block, forbidden) {
			t.Errorf("chooser touches %q", forbidden)
		}
	}
	if found := forbiddenSinksIn(block); len(found) != 0 {
		t.Errorf("chooser uses %q", found)
	}
}
