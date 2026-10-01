package webui

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestPreviewLineFragmentsOpenPinnedSource(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required for the external line-router check")
	}
	source := section(t, scriptSource(t), "  /* A fragment is not sent", "  /* Wrap switch")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	check := `
const assert = require('node:assert/strict');
const vm = require('node:vm');
const source = process.argv[1];
for (const home of ['/repositories/document', '/share/synthetic-document']) {
  for (const hashChange of [false, true]) {
    for (const line of [1, 12345, 13003]) {
      const href = 'https://example.invalid' + home + '/code?path=README.md';
      const target = home + '/code?path=README.md&view=source&revision=original&blob=original-blob';
      let navigation = null, listener = null;
      const location = { href, hash: hashChange ? '#heading' : '#L' + line, replace: address => { navigation = new URL(address); } };
      const panel = { getAttribute: name => name === 'data-line-page' ? target : null };
      vm.runInNewContext(source, { URL, window: { location, addEventListener: (event, callback) => { assert.equal(event, 'hashchange'); listener = callback; } }, document: { querySelector: () => panel, getElementById: () => null } });
      if (hashChange) { assert.equal(navigation, null); location.hash = '#L' + line; listener(); }
      assert.ok(navigation, home + ' preview did not route line ' + line);
      assert.equal(navigation.searchParams.get('line'), String(line));
      assert.equal(navigation.searchParams.get('view'), 'source');
      assert.equal(navigation.searchParams.get('revision'), 'original');
      assert.equal(navigation.searchParams.get('blob'), 'original-blob');
      assert.equal(navigation.hash, '#L' + line);
      assert.equal(navigation.pathname, home + '/code');
    }
  }
}
for (const hash of ['#L1', '#L0', '#Linvalid', '#heading']) {
  let moved = false;
  const panel = { getAttribute: name => ({ 'data-line-page': '/code?revision=original', 'data-line-first': '1', 'data-line-last': '10000', 'data-line-total': '13002' })[name] };
  vm.runInNewContext(source, { URL, window: { location: { href: 'https://example.invalid/code', hash, replace: () => { moved = true; } }, addEventListener: () => {} }, document: { querySelector: () => panel, getElementById: id => id === 'L1' ? {} : null } });
  assert.equal(moved, false, 'rendered or non-line anchor should not navigate');
}
vm.runInNewContext(source, { window: { addEventListener: () => { throw new Error('listener outside code view'); } }, document: { querySelector: () => null } });
`
	output, err := exec.CommandContext(ctx, node, "-e", check, source).CombinedOutput()
	if err != nil {
		t.Fatalf("external line-router check: %v\n%s", err, output)
	}
}

func TestErrorRecoveryLabelKeepsTheExistingDefault(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range []Lang{LangEN, LangKO} {
		page := ErrorPage{Chrome: fullChrome(lang), Status: 404, Code: MsgErrNotFound, RetryURL: "/"}
		out := render(t, r, page)
		if !strings.Contains(out, wantText(lang, MsgErrBackHome)) {
			t.Errorf("default error recovery label changed (%s)", lang)
		}
	}
}
