/* OwnGit dashboard behaviour.
 *
 * Every screen works without this file: links navigate, forms submit, and the
 * server renders the chosen language. The script only improves what a reload
 * would otherwise cost, and never invents text of its own.
 *
 * It does five things:
 *   1. Appearance: Light, Dark, or System, remembered per browser.
 *   2. Language: switch in place so typing in a form is not lost.
 *   3. Activity graph: arrow-key movement and a spoken day readout.
 *   4. Setup link: hold the one-time code in memory, clear it from the URL,
 *      and submit it only when the owner presses the start button.
 *   5. Sidebar, file view and diffs: fold the narrow-window menu, filter the
 *      repository list, close the file drawer, wrap long lines, fold every
 *      file of a diff at once, and follow a heading address written for
 *      GitHub to the matching heading of a rendered document.
 *
 * It never stores a password, a setup code, or any other secret.
 */

(function () {
  'use strict';

  var root = document.documentElement;

  /* ------------------------------------------------------------------ */
  /* small helpers                                                       */
  /* ------------------------------------------------------------------ */

  function all(selector, scope) {
    return Array.prototype.slice.call((scope || document).querySelectorAll(selector));
  }

  function readCookie(name) {
    var parts = document.cookie ? document.cookie.split('; ') : [];
    for (var i = 0; i < parts.length; i++) {
      var eq = parts[i].indexOf('=');
      if (eq > 0 && decodeURIComponent(parts[i].slice(0, eq)) === name) {
        return decodeURIComponent(parts[i].slice(eq + 1));
      }
    }
    return '';
  }

  // Preference cookies only. These are not credentials, so they carry no
  // authentication value and are readable by the page on purpose.
  function writeCookie(name, value) {
    var parts = [
      encodeURIComponent(name) + '=' + encodeURIComponent(value),
      'Path=/',
      'Max-Age=31536000',
      'SameSite=Lax'
    ];
    if (window.location.protocol === 'https:') { parts.push('Secure'); }
    document.cookie = parts.join('; ');
  }

  /* ------------------------------------------------------------------ */
  /* 1. appearance                                                       */
  /* ------------------------------------------------------------------ */

  /* System is the default and keeps following the operating system while it
   * stays selected. An explicit Light or Dark choice wins, because the CSS
   * rule for System is a media query on a different class.
   *
   * The choice lives in a preference cookie, so the server renders it and the
   * links work without this script. A choice made before the cookie existed
   * is still in this browser's storage and is read once, then moved. */

  var APPEARANCE_KEY = 'owngit_appearance';
  var darkQuery = window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null;

  function validAppearance(value) {
    return value === 'light' || value === 'dark' || value === 'system';
  }

  function currentAppearance() {
    var stored = readCookie(APPEARANCE_KEY);
    if (validAppearance(stored)) { return stored; }
    try { stored = window.localStorage.getItem(APPEARANCE_KEY) || ''; } catch (e) { stored = ''; }
    return stored === 'light' || stored === 'dark' ? stored : 'system';
  }

  function resolvedAppearance(choice) {
    if (choice === 'light' || choice === 'dark') { return choice; }
    return darkQuery && darkQuery.matches ? 'dark' : 'light';
  }

  function applyAppearance(choice) {
    root.classList.toggle('theme-light', choice === 'light');
    root.classList.toggle('theme-dark', choice === 'dark');
    root.classList.toggle('theme-system', choice === 'system');
    root.setAttribute('data-appearance', choice);
    root.setAttribute('data-appearance-resolved', resolvedAppearance(choice));
    all('[data-appearance-set]').forEach(function (link) {
      if (link.getAttribute('data-appearance-set') === choice) {
        link.setAttribute('aria-current', 'true');
      } else {
        link.removeAttribute('aria-current');
      }
    });
  }

  function setAppearance(choice) {
    writeCookie(APPEARANCE_KEY, choice);
    try { window.localStorage.removeItem(APPEARANCE_KEY); } catch (e) { /* private mode */ }
    applyAppearance(choice);
  }

  // Remove an appearance parameter the server has already saved, so a later
  // in-place choice is not undone by reloading the address.
  function dropAppearanceParameter() {
    if (!window.history || !window.history.replaceState) { return; }
    try {
      var url = new URL(window.location.href);
      if (!url.searchParams.has('appearance')) { return; }
      url.searchParams.delete('appearance');
      window.history.replaceState(window.history.state, '', url.pathname + url.search + url.hash);
    } catch (e) { /* older browser: the cookie still carries the choice */ }
  }

  var initialAppearance = currentAppearance();
  if (!validAppearance(readCookie(APPEARANCE_KEY)) && initialAppearance !== 'system') {
    setAppearance(initialAppearance);
  }
  applyAppearance(initialAppearance);
  dropAppearanceParameter();

  document.addEventListener('click', function (event) {
    var link = event.target.closest && event.target.closest('[data-appearance-set]');
    if (!link) { return; }
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) { return; }
    event.preventDefault();
    setAppearance(link.getAttribute('data-appearance-set'));
  });

  if (darkQuery) {
    var followSystem = function () {
      if (currentAppearance() === 'system') { applyAppearance('system'); }
    };
    if (darkQuery.addEventListener) {
      darkQuery.addEventListener('change', followSystem);
    } else if (darkQuery.addListener) {
      darkQuery.addListener(followSystem);
    }
  }

  /* ------------------------------------------------------------------ */
  /* 2. language                                                         */
  /* ------------------------------------------------------------------ */

  /* The server renders every translatable string in both languages on the
   * element itself (data-en / data-ko, and data-en-title / data-ko-title for
   * text attributes). Switching copies one into place. Nothing is translated
   * here, so the two languages cannot drift from the server's catalogue, and
   * input values, focus, scroll position and the current wizard step survive.
   *
   * Without JavaScript the same links are ordinary navigation, which the
   * backend answers with the other language. */

  var TEXT_ATTRS = ['placeholder', 'title', 'aria-label', 'value', 'alt'];

  /* target is the link the reader followed, when there was one. Its href is
   * the server's own address for this screen in the chosen language, so it is
   * what the address bar should end up showing.
   *
   * Deriving the address from window.location instead was a real defect. A
   * screen rendered from a POST, such as the restore preview, has a request
   * address that only accepts POST. Rewriting that address with a language
   * parameter left the reader on a URL that answers 404 when reloaded or
   * shared, and dropped the selection the link carried. */
  function applyLanguage(lang, target) {
    var other = lang === 'ko' ? 'en' : 'ko';

    all('[data-' + lang + ']').forEach(function (node) {
      var text = node.getAttribute('data-' + lang);
      if (text !== null) { node.textContent = text; }
    });

    TEXT_ATTRS.forEach(function (attr) {
      all('[data-' + lang + '-' + attr + ']').forEach(function (node) {
        var text = node.getAttribute('data-' + lang + '-' + attr);
        if (text !== null) { node.setAttribute(attr, text); }
      });
    });

    var title = root.getAttribute('data-title-' + lang);
    if (title) { document.title = title; }

    root.setAttribute('lang', lang);
    root.setAttribute('data-lang', lang);

    all('[data-lang-set]').forEach(function (link) {
      var on = link.getAttribute('data-lang-set') === lang;
      if (on) {
        link.setAttribute('aria-current', 'true');
      } else {
        link.removeAttribute('aria-current');
      }
    });

    // Keep the search form and any other lang field in step, so a later
    // ordinary navigation stays in the chosen language.
    all('input[name="lang"]').forEach(function (field) { field.value = lang; });

    // Update the address bar without navigating, so a reload or a shared link
    // keeps both the language and the screen the reader is looking at.
    if (window.history && window.history.replaceState) {
      try {
        var href = target && target.getAttribute && target.getAttribute('href');
        var url = new URL(href || window.location.href, window.location.href);
        url.searchParams.set('lang', lang);
        window.history.replaceState(window.history.state, '', url.pathname + url.search + url.hash);
      } catch (e) { /* older browser: the cookie still carries the choice */ }
    }

    writeCookie(root.getAttribute('data-lang-cookie') || 'owngit_lang', lang);
    void other;
  }

  document.addEventListener('click', function (event) {
    var link = event.target.closest && event.target.closest('[data-lang-set]');
    if (!link) { return; }
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) { return; }
    event.preventDefault();
    applyLanguage(link.getAttribute('data-lang-set'), link);
  });

  /* ------------------------------------------------------------------ */
  /* 3. activity graph                                                   */
  /* ------------------------------------------------------------------ */

  /* The grid is drawn row-major: one row per weekday, one column per week, so
   * the reading order matches what is on screen. Arrow keys move a single
   * roving tab stop between days.
   *
   * Each day is a link to the filtered activity list, which is what a click or
   * Enter follows, with or without this script. All this adds is arrow-key
   * movement and a spoken summary of the focused day, so a reader can survey
   * the year without loading a page per day. */

  all('[data-graph]').forEach(function (graph) {
    var cells = all('[data-day]', graph);
    if (!cells.length) { return; }

    var readout = graph.querySelector('[data-graph-readout]');
    var weeks = parseInt(graph.getAttribute('data-weeks') || '0', 10);
    var focused = null;

    cells.forEach(function (cell) {
      cell.setAttribute('tabindex', '-1');
    });

    var initial = graph.querySelector('[data-day][aria-current="true"]') || cells[cells.length - 1];
    initial.setAttribute('tabindex', '0');
    focused = initial;

    function focusCell(cell) {
      if (!cell) { return; }
      focused.setAttribute('tabindex', '-1');
      cell.setAttribute('tabindex', '0');
      cell.focus();
      focused = cell;
      announce(cell);
    }

    function cellAt(index) {
      for (var i = 0; i < cells.length; i++) {
        if (parseInt(cells[i].getAttribute('data-index'), 10) === index) { return cells[i]; }
      }
      return null;
    }

    // Speak the focused day without claiming it was opened. Following the
    // link is what actually filters the list.
    function announce(cell) {
      if (!readout) { return; }
      var lang = root.getAttribute('data-lang') === 'ko' ? 'ko' : 'en';
      var text = cell.getAttribute('data-' + lang + '-aria-label') || cell.getAttribute('aria-label') || '';
      readout.textContent = text;
      readout.setAttribute('data-en', cell.getAttribute('data-en-aria-label') || text);
      readout.setAttribute('data-ko', cell.getAttribute('data-ko-aria-label') || text);
    }

    graph.addEventListener('keydown', function (event) {
      var cell = event.target.closest && event.target.closest('[data-day]');
      if (!cell) { return; }
      var index = parseInt(cell.getAttribute('data-index'), 10);
      var next = null;
      switch (event.key) {
        case 'ArrowLeft': next = cellAt(index - 7); break;
        case 'ArrowRight': next = cellAt(index + 7); break;
        case 'ArrowUp': next = cellAt(index - 1); break;
        case 'ArrowDown': next = cellAt(index + 1); break;
        case 'Home': next = cells[0]; break;
        case 'End': next = cells[cells.length - 1]; break;
        // Enter and Space are left alone: the day is a link, and the browser
        // already follows it.
        default:
          return;
      }
      if (next) {
        event.preventDefault();
        focusCell(next);
      }
    });

    // Move the tab stop to whatever the reader last touched, so returning by
    // keyboard resumes there. The click itself still follows the link.
    graph.addEventListener('mousedown', function (event) {
      var cell = event.target.closest && event.target.closest('[data-day]');
      if (!cell || cell === focused) { return; }
      focused.setAttribute('tabindex', '-1');
      cell.setAttribute('tabindex', '0');
      focused = cell;
    });

    void weeks;
  });

  /* ------------------------------------------------------------------ */
  /* 4. setup link                                                       */
  /* ------------------------------------------------------------------ */

  /* The one-time code arrives in the URL fragment, which browsers do not send
   * to the server. The code is moved into a form field in memory, the address
   * bar is cleaned immediately, and nothing is written to storage or logged.
   * Opening or previewing the page does not redeem anything: the owner has to
   * press the start button, which posts the code. */

  (function handleSetupFragment() {
    var form = document.querySelector('[data-redeem-form]');
    if (!form) { return; }

    var field = form.querySelector('input[name="token"]');
    var start = form.querySelector('[data-redeem-start]');
    var missing = form.querySelector('[data-redeem-missing]');
    var held = form.querySelector('[data-redeem-held]');
    var fragment = window.location.hash ? window.location.hash.slice(1) : '';
    var token = '';

    if (fragment) {
      var params = new URLSearchParams(fragment);
      token = params.get('token') || (fragment.indexOf('=') === -1 ? fragment : '');
    }

    if (token && field) { field.value = token; }

    // The server rendered both statements hidden because it cannot see the
    // fragment. Exactly one is shown here, and the button is enabled only
    // when a code is actually in hand.
    var haveToken = !!(field && field.value);
    if (held) { held.hidden = !haveToken; }
    if (missing) { missing.hidden = haveToken; }
    if (start) {
      if (haveToken) {
        start.removeAttribute('disabled');
      } else {
        start.setAttribute('disabled', 'disabled');
      }
    }

    // Clear the fragment whether or not it held a code, so the address bar,
    // the history entry, and anything the reader copies stay clean.
    if (window.location.hash && window.history && window.history.replaceState) {
      window.history.replaceState(
        window.history.state,
        '',
        window.location.pathname + window.location.search
      );
    }
  })();

  /* Settings: expand the form whose control the reader activated, and keep the
   * requested one open after a failed submission. */

  all('[data-disclosure]').forEach(function (details) {
    var action = details.getAttribute('data-disclosure');
    if (details.getAttribute('data-disclosure-open') === action) { details.open = true; }
  });

  /* The ref picker submits on change once scripting is available, so the
   * separate button is only needed without it. */

  all('[data-hide-with-script]').forEach(function (button) { button.hidden = true; });

  all('[data-submit-on-change]').forEach(function (select) {
    select.addEventListener('change', function () {
      var form = select.form;
      if (!form) { return; }
      if (form.requestSubmit) { form.requestSubmit(); } else { form.submit(); }
    });
  });

  /* Restore: show which of the two scopes the per-file ticks belong to.
   *
   * The radio is what the backend reads, with or without this script, so the
   * ticks are only marked inactive, never disabled or cleared. A reader who
   * chose the whole project can still read the list to see what that will do,
   * and switching back finds their earlier ticks intact.
   *
   * The marking is an attribute the stylesheet reads. It changes the panel's
   * surface and reveals a line of text; it never fades the text itself, so
   * every word in here keeps its normal contrast. */

  all('[data-restore-files]').forEach(function (panel) {
    var form = panel.closest && panel.closest('form');
    if (!form) { return; }
    var scopes = all('[data-restore-scope]', form);
    if (!scopes.length) { return; }

    function sync() {
      var picked = form.querySelector('[data-restore-scope]:checked');
      var files = picked && picked.getAttribute('data-restore-scope') === 'files';
      if (files) {
        panel.removeAttribute('data-restore-dimmed');
      } else {
        panel.setAttribute('data-restore-dimmed', '');
      }
    }

    scopes.forEach(function (scope) { scope.addEventListener('change', sync); });

    // Ticking a path is a statement that those paths are what should be
    // restored, so it selects the matching scope rather than being read under
    // a scope that ignores it.
    all('input[type="checkbox"][name="path"]', panel).forEach(function (box) {
      box.addEventListener('change', function () {
        if (!box.checked) { return; }
        var files = form.querySelector('[data-restore-scope="files"]');
        if (files && !files.checked) {
          files.checked = true;
          sync();
        }
      });
    });

    sync();
  });

  /* Select a one-time secret when it is focused, so it can be copied in one
   * gesture. The value is already on the page and stays selectable by hand
   * without this; nothing here stores, sends, or logs it. */

  all('[data-select-on-focus]').forEach(function (field) {
    field.addEventListener('focus', function () {
      if (field.select) { field.select(); }
    });
  });

  /* Copy an example to the clipboard.
   *
   * The button is rendered hidden and shown only here, because without a
   * script it would do nothing; the example itself stays selectable text.
   * The result is announced in the page's current language from words the
   * server rendered, and the words are also set as data-en/data-ko so a later
   * language switch rewrites them like any other text. */

  all('[data-copy-target]').forEach(function (button) {
    var source = document.getElementById(button.getAttribute('data-copy-target'));
    var status = button.parentNode && button.parentNode.querySelector('[data-copy-status]');
    if (!source) { return; }
    button.hidden = false;

    function say(kind) {
      if (!status) { return; }
      var lang = root.getAttribute('data-lang') === 'ko' ? 'ko' : 'en';
      var en = status.getAttribute('data-copy-' + kind + '-en') || '';
      var ko = status.getAttribute('data-copy-' + kind + '-ko') || '';
      status.setAttribute('data-en', en);
      status.setAttribute('data-ko', ko);
      status.textContent = lang === 'ko' ? ko : en;
    }

    function selectSource() {
      var range = document.createRange();
      range.selectNodeContents(source);
      var selection = window.getSelection();
      selection.removeAllRanges();
      selection.addRange(range);
    }

    button.addEventListener('click', function () {
      var text = source.textContent;
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(function () { say('ok'); }, function () {
          selectSource();
          say('fail');
        });
      } else {
        selectSource();
        say('fail');
      }
    });
  });

  /* Clone address copy button. The address is a read-only field that can be
   * selected by hand, so the button is rendered hidden and only revealed
   * here. Both outcomes are server-rendered in both languages; the script
   * chooses which one to show and never writes text of its own. */

  all('[data-clone]').forEach(function (box) {
    var button = box.querySelector('[data-copy]');
    var field = button && document.getElementById(button.getAttribute('data-copy'));
    if (!field) { return; }
    var done = box.querySelector('[data-copy-done]');
    var fail = box.querySelector('[data-copy-fail]');
    var timer = null;

    function show(ok) {
      if (done) { done.hidden = !ok; }
      if (fail) { fail.hidden = ok; }
      window.clearTimeout(timer);
      timer = window.setTimeout(function () {
        if (done) { done.hidden = true; }
        if (fail) { fail.hidden = true; }
      }, 4000);
    }

    function fallback() {
      field.select();
      var ok = false;
      try { ok = document.execCommand('copy'); } catch (error) { ok = false; }
      show(ok);
    }

    button.hidden = false;
    button.addEventListener('click', function () {
      if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(field.value).then(function () { show(true); }, fallback);
      } else {
        fallback();
      }
    });
  });

  /* Sidebar: bring the open repository into view inside the list on load.
   * On a wide screen the list scrolls on its own, so a repository far down
   * would otherwise be selected but out of sight. Only the list's own
   * scrollTop moves; the page and focus stay where they are. */

  var sideList = document.querySelector('.sidebar__inner');
  var sideCurrent = sideList && sideList.querySelector('.sb__item[aria-current="page"]');
  if (sideCurrent && sideList.scrollHeight > sideList.clientHeight) {
    var listBox = sideList.getBoundingClientRect();
    var itemBox = sideCurrent.getBoundingClientRect();
    var visibleBottom = Math.min(listBox.bottom, window.innerHeight);
    if (itemBox.bottom > visibleBottom || itemBox.top < listBox.top) {
      sideList.scrollTop += itemBox.top - listBox.top - (visibleBottom - listBox.top - itemBox.height) / 2;
    }
  }

  /* ------------------------------------------------------------------ */
  /* 5. sidebar, file view and diffs                                     */
  /* ------------------------------------------------------------------ */

  /* Narrow window: fold the sidebar behind its menu button. The button is
   * rendered hidden and the menu open, so without this file the menu is
   * simply shown in full. The fold itself only applies below 900px. */

  var sidebar = document.querySelector('[data-sidebar]');
  var sideToggle = sidebar && sidebar.querySelector('[data-sidebar-toggle]');
  if (sideToggle) {
    var setSideOpen = function (open) {
      sidebar.classList.toggle('is-open', open);
      sideToggle.setAttribute('aria-expanded', String(open));
    };
    sidebar.classList.add('sidebar--folds');
    sideToggle.hidden = false;
    sideToggle.addEventListener('click', function () {
      setSideOpen(!sidebar.classList.contains('is-open'));
    });
    sidebar.addEventListener('keydown', function (event) {
      if (event.key !== 'Escape' || !sidebar.classList.contains('is-open')) { return; }
      if (sideToggle.offsetParent === null) { return; } // wide window: nothing is folded
      setSideOpen(false);
      sideToggle.focus();
    });
  }

  /* Repository filter. It only hides rows already on the page, so nothing is
   * written into the field or the list while the reader types, which keeps
   * an input method's composition intact. */

  var sideFilter = document.querySelector('[data-sb-filter]');
  if (sideFilter) {
    var sideRows = all('[data-sb-name]');
    var noMatch = document.querySelector('[data-sb-nomatch]');
    sideFilter.hidden = false;
    sideFilter.addEventListener('input', function () {
      var wanted = sideFilter.value.trim().toLowerCase();
      var shown = 0;
      sideRows.forEach(function (row) {
        var hit = !wanted || row.getAttribute('data-sb-name').toLowerCase().indexOf(wanted) !== -1;
        row.hidden = !hit;
        if (hit) { shown++; }
      });
      if (noMatch) { noMatch.hidden = shown !== 0; }
    });
  }

  /* File list drawer. It is a details element, so it opens and closes
   * without this file. Escape and a click elsewhere close it too, and focus
   * goes back to its button when it was inside. */

  all('[data-drawer]').forEach(function (drawer) {
    var summary = drawer.querySelector('summary');
    drawer.addEventListener('keydown', function (event) {
      if (event.key !== 'Escape' || !drawer.open) { return; }
      drawer.open = false;
      if (summary) { summary.focus(); }
    });
    document.addEventListener('click', function (event) {
      if (drawer.open && !drawer.contains(event.target)) { drawer.open = false; }
    });
  });

  /* Wrap switch for code and diffs. Long lines scroll sideways by default;
   * the switch wraps them, and the choice is remembered in this browser. The
   * button is rendered hidden, because without this file it could not work. */

  var WRAP_KEY = 'owngit_wrap';
  var wrapButtons = all('[data-wrap-toggle]');
  if (wrapButtons.length) {
    var applyWrap = function (on) {
      if (on) { root.setAttribute('data-wrap', '1'); } else { root.removeAttribute('data-wrap'); }
      wrapButtons.forEach(function (button) { button.setAttribute('aria-pressed', String(on)); });
    };
    var storedWrap = '';
    try { storedWrap = window.localStorage.getItem(WRAP_KEY) || ''; } catch (e) { storedWrap = ''; }
    applyWrap(storedWrap === '1');
    wrapButtons.forEach(function (button) {
      button.hidden = false;
      button.addEventListener('click', function () {
        var on = root.getAttribute('data-wrap') !== '1';
        try { window.localStorage.setItem(WRAP_KEY, on ? '1' : '0'); } catch (e) { /* private mode */ }
        applyWrap(on);
      });
    });
  }

  /* Diff files fold one at a time or all at once. The fold buttons are
   * rendered hidden, so without this file every diff is simply open. Following
   * a link to a folded file from the file list opens it again. */

  function setFolded(file, folded) {
    var button = file.querySelector('[data-diff-fold]');
    file.classList.toggle('is-folded', folded);
    if (button) { button.setAttribute('aria-expanded', String(!folded)); }
  }

  all('[data-diff]').forEach(function (scope) {
    var files = all('.dfile', scope);
    var toggleAll = scope.querySelector('[data-diff-all]');
    var collapse = toggleAll && toggleAll.querySelector('[data-diff-collapse]');
    var expand = toggleAll && toggleAll.querySelector('[data-diff-expand]');
    var anyOpen = function () {
      return files.some(function (file) { return !file.classList.contains('is-folded'); });
    };
    var sync = function () {
      var open = anyOpen();
      if (collapse) { collapse.hidden = !open; }
      if (expand) { expand.hidden = open; }
    };

    files.forEach(function (file) {
      var button = file.querySelector('[data-diff-fold]');
      if (!button) { return; }
      button.hidden = false;
      button.addEventListener('click', function () {
        setFolded(file, !file.classList.contains('is-folded'));
        sync();
      });
    });

    if (toggleAll && files.length) {
      toggleAll.hidden = false;
      sync();
      toggleAll.addEventListener('click', function () {
        var fold = anyOpen();
        files.forEach(function (file) { setFolded(file, fold); });
        sync();
      });
    }

    scope.addEventListener('click', function (event) {
      var link = event.target.closest && event.target.closest('.dlist__row');
      if (!link) { return; }
      var target = document.getElementById((link.getAttribute('href') || '').slice(1));
      if (!target || !target.classList.contains('is-folded')) { return; }
      setFolded(target, false);
      sync();
    });
  });

  // A rendered document's heading anchors carry an "md-" prefix, so they
  // never take an id of the page itself. An address copied from GitHub names
  // the bare, possibly capitalised anchor; find the prefixed heading instead.
  function revealHeading() {
    var id;
    try { id = decodeURIComponent(window.location.hash.slice(1)); } catch (e) { return; }
    if (!id || document.getElementById(id)) { return; }
    id = id.toLowerCase();
    // A heading whose own anchor starts with "md-" has the prefix twice, so
    // the prefixed form is tried first and the address as written second.
    var heading = document.getElementById('md-' + id) || document.getElementById(id);
    if (heading && heading.closest('.md')) { heading.scrollIntoView(); }
  }
  if (document.querySelector('.md')) {
    revealHeading();
    // Fonts and images that finish loading move the heading, so it is
    // aligned again once the page has loaded.
    window.addEventListener('load', revealHeading);
    window.addEventListener('hashchange', revealHeading);
  }
})();
