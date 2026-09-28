/* OwnGit dashboard behaviour.
 *
 * Every screen works without this file: links navigate, forms submit, and the
 * server renders the chosen language. The script only improves what a reload
 * would otherwise cost, and never invents text of its own.
 *
 * It does six things:
 *   1. Appearance: Light, Dark, or System, remembered per browser.
 *   2. Language: switch in place so typing in a form is not lost.
 *   3. Activity graph: arrow-key movement and a spoken day readout.
 *   4. Setup link: hold the one-time code in memory, clear it from the URL,
 *      and submit it only when the owner presses the start button.
 *   5. Sidebar, file view and diffs: fold the narrow-window menu, filter the
 *      repository list, close the file drawer, wrap long lines, fold every
 *      file of a diff at once, and follow a heading address written for
 *      GitHub to the matching heading of a rendered document.
 *   6. Repository list order: reorder the dashboard list and the sidebar in
 *      place, remember the choice per browser, and keep the order right when
 *      the language changes.
 *   7. Settings: apply this browser's display choices at once, show which
 *      settings group holds a change, and save a group whose form has no
 *      password field without leaving the page, so the other groups keep
 *      what was typed in them.
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
    all('[data-pref="appearance"]').forEach(function (select) { select.value = choice; });
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

  var TEXT_ATTRS = ['placeholder', 'title', 'aria-label', 'value', 'alt', 'label'];

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
    all('[data-pref="lang"]').forEach(function (select) { select.value = lang; });

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
    // Name order depends on the language, so the lists follow it.
    all('[data-order-list]').forEach(orderRows);
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
    // when a code is actually in hand. While setup runs in the terminal the
    // form is optional and appears only when the address held a code.
    var haveToken = !!(field && field.value);
    if (form.hasAttribute('data-redeem-optional')) { form.hidden = !haveToken; }
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

  /* Browser approval: while the terminal decides, ask the server every two
   * seconds for the state of this browser's own request. The request is a
   * GET with no body, sends only this site's cookies, and redeems nothing.
   * The status line, a polite live region, changes only when the answer
   * arrives, so a screen reader hears it once; then the setup page opens.
   * Without scripting, Check again does the same. */

  (function watchApproval() {
    var status = document.querySelector('[data-approval-wait]');
    if (!status || !window.fetch) { return; }
    var statusURL = status.getAttribute('data-status-url');
    var nextURL = status.getAttribute('data-next-url');
    var answered = document.querySelector('[data-approval-answered]');
    function poll() {
      window.fetch(statusURL, { method: 'GET', credentials: 'same-origin', cache: 'no-store' })
        .then(function (response) { return response.ok ? response.json() : null; })
        .then(function (reply) {
          if (!reply || reply.state === 'pending') {
            window.setTimeout(poll, reply ? 2000 : 5000);
            return;
          }
          if (answered) { status.textContent = answered.textContent; }
          window.location.replace(nextURL);
        })
        .catch(function () { window.setTimeout(poll, 5000); });
    }
    window.setTimeout(poll, 2000);
  })();

  /* Settings: expand the form whose control the reader activated, and keep the
   * requested one open after a failed submission. */

  all('[data-disclosure]').forEach(function (details) {
    var action = details.getAttribute('data-disclosure');
    if (details.getAttribute('data-disclosure-open') === action) { details.open = true; }
  });

  /* The ref picker opens the chosen ref as soon as a pointer picks it, and
   * on Enter. A change made with arrow keys only moves the selection: on a
   * closed select those keys change the value one step at a time, and
   * loading a page for every step would make a far branch unreachable.
   * The visible button submits in every case, including without scripting. */

  all('[data-submit-on-change]').forEach(function (select) {
    var pointer = false;
    function submit() {
      var form = select.form;
      if (!form) { return; }
      if (form.requestSubmit) { form.requestSubmit(); } else { form.submit(); }
    }
    select.addEventListener('pointerdown', function () { pointer = true; });
    select.addEventListener('keydown', function (event) {
      pointer = false;
      if (event.key === 'Enter') {
        event.preventDefault();
        submit();
      }
    });
    select.addEventListener('change', function () {
      if (pointer) { submit(); }
    });
  });

  /* Import credentials: show and send only the chosen form's fields.
   *
   * The stylesheet already hides the other forms' fields; disabling them
   * here also keeps a value typed before switching from being sent with the
   * form that was chosen afterwards. Nothing is cleared, so switching back
   * finds what was typed. */

  all('[data-cred-form]').forEach(function (select) {
    var form = select.form;
    if (!form) { return; }
    var groups = all('[data-cred-for]', form);
    function sync() {
      groups.forEach(function (group) {
        var on = group.getAttribute('data-cred-for') === select.value;
        group.hidden = !on;
        all('input, textarea', group).forEach(function (field) { field.disabled = !on; });
      });
    }
    select.addEventListener('change', sync);
    sync();
  });

  /* Restore: show the file list only while "Selected files" is chosen.
   *
   * The stylesheet already does this from the radio's state; hidden covers a
   * browser without :has(). The radio is what the backend reads, so the
   * ticks are only hidden, never disabled or cleared, and switching back
   * finds them as they were. */

  all('[data-restore-files]').forEach(function (panel) {
    var form = panel.closest && panel.closest('form');
    if (!form) { return; }
    var scopes = all('[data-restore-scope]', form);
    if (!scopes.length) { return; }

    function sync() {
      var picked = form.querySelector('[data-restore-scope]:checked');
      panel.hidden = !(picked && picked.getAttribute('data-restore-scope') === 'files');
    }

    scopes.forEach(function (scope) { scope.addEventListener('change', sync); });
    sync();
  });

  /* Select a one-time secret when it is focused, so it can be copied in one
   * gesture. The value is already on the page and stays selectable by hand
   * without this; nothing here stores, sends, or logs it. */

  function selectOnFocus(scope) {
    all('[data-select-on-focus]', scope).forEach(function (field) {
      field.addEventListener('focus', function () {
        if (field.select) { field.select(); }
      });
    });
  }
  selectOnFocus(document);

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

  function cloneField(box) {
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
  }
  all('[data-clone]').forEach(cloneField);

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

  /* ------------------------------------------------------------------ */
  /* 6. repository list order                                            */
  /* ------------------------------------------------------------------ */

  /* The server sorts both lists, so the order is right without this file,
   * and remembers the choice in a preference cookie. Here a change reorders
   * the rows in place, so focus stays on the control.
   *
   * Each row carries what ordering needs: data-updated (seconds, empty when
   * the row shows no time) and data-rank-en / data-rank-ko, its position in
   * name order in each language. The server computed those positions with
   * its one collation, so the script only compares numbers and cannot
   * disagree with a reload. */

  var ORDER_KEY = 'owngit_order';

  function validOrder(value) {
    return value === 'updated-desc' || value === 'updated-asc' || value === 'name-asc' || value === 'name-desc';
  }

  function orderRows(list) {
    var order = list.getAttribute('data-order');
    if (!validOrder(order)) { return; }
    var lang = root.getAttribute('data-lang') === 'ko' ? 'ko' : 'en';
    var rank = function (row) { return parseInt(row.getAttribute('data-rank-' + lang), 10) || 0; };
    var time = function (row) {
      var value = row.getAttribute('data-updated');
      return value ? parseInt(value, 10) : null;
    };
    var rows = Array.prototype.filter.call(list.children, function (child) {
      return child.hasAttribute('data-order-item');
    });
    rows.sort(function (a, b) {
      if (order === 'name-asc') { return rank(a) - rank(b); }
      if (order === 'name-desc') { return rank(b) - rank(a); }
      var ta = time(a), tb = time(b);
      // A row without a time comes last in both directions.
      if ((ta === null) !== (tb === null)) { return ta === null ? 1 : -1; }
      if (ta !== null && ta !== tb) { return order === 'updated-asc' ? ta - tb : tb - ta; }
      return rank(a) - rank(b);
    });
    // Anything after the rows, such as the "no match" line, stays after them.
    var rest = Array.prototype.filter.call(list.children, function (child) {
      return !child.hasAttribute('data-order-item');
    })[0] || null;
    var focused = document.activeElement;
    rows.forEach(function (row) { list.insertBefore(row, rest); });
    if (focused && focused !== document.activeElement && list.contains(focused)) {
      focused.focus({ preventScroll: true });
    }
  }

  function optionText(option, lang) {
    return option.getAttribute('data-' + lang) || option.textContent;
  }

  /* Remember a new order, reorder every list on the page and say the order
   * in words where the sidebar names it. option is the chosen option, whose
   * server-rendered text in both languages names the order. */
  function applyOrder(order, option) {
    if (!validOrder(order)) { return false; }
    writeCookie(ORDER_KEY, order);
    all('[data-order-list]').forEach(function (list) {
      list.setAttribute('data-order', order);
      orderRows(list);
    });
    var lang = root.getAttribute('data-lang') === 'ko' ? 'ko' : 'en';
    all('[data-order-name]').forEach(function (node) {
      node.setAttribute('data-en', optionText(option, 'en'));
      node.setAttribute('data-ko', optionText(option, 'ko'));
      node.textContent = optionText(option, lang);
    });
    all('[data-pref="order"]').forEach(function (select) { select.value = order; });
    return true;
  }

  var orderForm = document.querySelector('[data-order-form]');
  var orderSelect = orderForm && orderForm.querySelector('[data-order-select]');
  if (orderSelect) {
    var orderApply = orderForm.querySelector('[data-order-apply]');
    var orderStatus = orderForm.querySelector('[data-order-status]');
    if (orderApply) { orderApply.hidden = true; }

    // Drop an order parameter the server has already saved, so reloading the
    // address does not undo a later in-place choice.
    if (window.history && window.history.replaceState) {
      try {
        var orderURL = new URL(window.location.href);
        if (orderURL.searchParams.has('order')) {
          orderURL.searchParams.delete('order');
          window.history.replaceState(window.history.state, '', orderURL.pathname + orderURL.search + orderURL.hash);
        }
      } catch (e) { /* older browser: the cookie still carries the choice */ }
    }

    orderSelect.addEventListener('change', function () {
      var option = orderSelect.options[orderSelect.selectedIndex];
      if (!applyOrder(orderSelect.value, option) || !orderStatus) { return; }
      // Tell assistive technology, from the option's own text.
      var lang = root.getAttribute('data-lang') === 'ko' ? 'ko' : 'en';
      var sayEN = (orderStatus.getAttribute('data-prefix-en') || '') + ' ' + optionText(option, 'en');
      var sayKO = (orderStatus.getAttribute('data-prefix-ko') || '') + ' ' + optionText(option, 'ko');
      orderStatus.setAttribute('data-en', sayEN);
      orderStatus.setAttribute('data-ko', sayKO);
      orderStatus.textContent = lang === 'ko' ? sayKO : sayEN;
    });
  }

  /* ------------------------------------------------------------------ */
  /* 7. settings                                                         */
  /* ------------------------------------------------------------------ */

  /* Display choices belong to this browser, like the buttons at the top of
   * the page, so a choice applies at once and Apply is hidden. */

  var displayForm = document.querySelector('[data-display-form]');
  if (displayForm) {
    all('[data-display-apply]', displayForm).forEach(function (node) { node.hidden = true; });
    displayForm.addEventListener('change', function (event) {
      var select = event.target;
      var pref = select.getAttribute ? select.getAttribute('data-pref') : '';
      if (pref === 'lang') {
        applyLanguage(select.value, null);
      } else if (pref === 'appearance' && validAppearance(select.value)) {
        setAppearance(select.value);
      } else if (pref === 'order') {
        applyOrder(select.value, select.options[select.selectedIndex]);
      }
    });
  }

  /* Settings groups. settings.html describes the markup. A group holds a
   * change while one of its setting controls differs from its saved value;
   * only then its "Not saved" mark and its save bar are shown.
   *
   * A group whose form has no password field is saved without leaving the
   * page (see groupSave below and settings.go): a saved change comes back
   * as the address that shows it, and this page then takes the saved
   * group, and every group without a change, from that address. A refused
   * change comes back as the tab with that group showing why. Every other
   * group is left as it is, with what was typed in it. A form with a
   * password field, the administrator password that confirms a save
   * included, is always submitted by the browser as a whole page.
   *
   * These functions are the whole interface: groupDirty, discardGroup and
   * saveGroup, which resolves to true once the change is saved. */

  var settingsPanel = document.querySelector('[data-settings]');

  function groupForm(group) {
    return group.querySelector('[data-group-form]');
  }

  // The form's address. form.action would be its field named "action".
  function formAddress(form) {
    return new URL(form.getAttribute('action') || '', window.location.href).href;
  }

  // The controls that hold a setting. A hidden field, a button, a gate
  // such as the administrator password, and anything not shown are not.
  function settingControls(form) {
    return Array.prototype.filter.call(form.elements, function (control) {
      if (!control.name || control.disabled || control.type === 'hidden' ||
          control.type === 'submit' || control.type === 'button') { return false; }
      return !control.hasAttribute('data-group-gate') && !control.closest('[hidden]');
    });
  }

  // A password field has no saved value, and its value is never read: it
  // is a change once something was typed in it (data-typed).
  function currentValue(control) {
    if (control.type === 'password') { return control.hasAttribute('data-typed') ? 'typed' : ''; }
    if (control.type === 'checkbox') { return control.checked ? 'on' : 'off'; }
    return String(control.value).replace(/\r\n/g, '\n');
  }

  function savedValue(control) {
    if (control.type === 'password') { return ''; }
    return (control.getAttribute('data-saved') || '').replace(/\r\n/g, '\n');
  }

  function groupDirty(group) {
    var form = groupForm(group);
    return !!form && settingControls(form).some(function (control) {
      return currentValue(control) !== savedValue(control);
    });
  }

  // What the named field of form sends now: "on" or "off" for a switch.
  function sentValue(form, name) {
    var control = all('[name="' + name + '"]', form).filter(function (field) { return !field.disabled; })[0];
    if (!control) { return ''; }
    return control.type === 'checkbox' ? (control.checked ? 'on' : 'off') : control.value;
  }

  function syncGroup(group) {
    var form = groupForm(group);
    if (!form) { return; }
    all('[data-show-if]', form).forEach(function (node) {
      var rule = node.getAttribute('data-show-if').split('=');
      node.hidden = sentValue(form, rule[0]) !== rule[1];
    });
    var dirty = groupDirty(group);
    var open = dirty || group.hasAttribute('data-group-refused') || group.hasAttribute('data-group-open');
    group.classList.toggle('is-dirty', dirty);
    all('[data-group-mark]', group).forEach(function (node) { node.hidden = !dirty; });
    all('[data-group-bar-title]', group).forEach(function (node) { node.hidden = !dirty; });
    all('[data-group-bar]', group).forEach(function (node) { node.hidden = !open; });
  }

  // Put the saved values back, as they were when the page was shown.
  function discardGroup(group) {
    var form = groupForm(group);
    if (!form) { return; }
    Array.prototype.forEach.call(form.elements, function (control) {
      if (!control.name || control.type === 'hidden' || control.type === 'submit' || control.type === 'button') { return; }
      if (control.type === 'password') {
        control.value = '';
        control.removeAttribute('data-typed');
      } else if (control.type === 'checkbox') {
        control.checked = control.getAttribute('data-saved') === 'on';
      } else if (control.hasAttribute('data-saved')) {
        control.value = control.getAttribute('data-saved');
      }
    });
    all('details[data-group-details]', group).forEach(function (details) { details.open = false; });
    all('[data-settings-note]', group).forEach(function (note) { note.parentNode.removeChild(note); });
    syncGroup(group);
  }

  /* groupSave is the only part of this file, besides the approval watcher,
   * that sends a request, and a security review checks it on its own
   * (asset_pins_test.go pins it). It sends a group's form only when the
   * form has no password field, so it never builds the form data of a form
   * that holds a password. It POSTs to the form's own address on this
   * site, and after a saved change makes one GET of the address the server
   * returned, again only on this site. Both send only this site's cookies
   * and follow no redirect. It copies the anti-forgery token of the fetched
   * page into this page's forms, because a saved change can end the
   * session that token belongs to. It stores nothing. */
  var groupSave = (function groupSave() {
    if (!window.fetch || !window.DOMParser || !window.FormData || !window.URLSearchParams) { return null; }

    // The address, on this site only, or null.
    function onThisSite(address) {
      var target;
      try { target = new URL(address, window.location.href); } catch (e) { return null; }
      return target.origin === window.location.origin ? target : null;
    }

    function holdsPassword(form) {
      return Array.prototype.some.call(form.elements, function (control) { return control.type === 'password'; });
    }

    function eligible(form) {
      return !holdsPassword(form) && !!onThisSite(formAddress(form));
    }

    // Send the form. Rejects, sending nothing, when the form may not be
    // sent this way.
    function send(form, name) {
      var target = onThisSite(formAddress(form));
      if (holdsPassword(form) || !target) { return Promise.reject(new Error('not sent')); }
      return window.fetch(target.href, {
        method: 'POST',
        body: new URLSearchParams(new FormData(form)),
        credentials: 'same-origin',
        cache: 'no-store',
        redirect: 'manual',
        headers: { 'X-OwnGit-Group': name }
      });
    }

    // Read the page at the address a saved change returned.
    function read(address) {
      var target = onThisSite(address);
      if (!target) { return Promise.reject(new Error('not read')); }
      return window.fetch(target.href, { method: 'GET', credentials: 'same-origin', cache: 'no-store', redirect: 'manual' })
        .then(function (response) {
          if (response.status !== 200) { throw new Error('not read'); }
          return response.text();
        });
    }

    function copyToken(doc) {
      var token = doc.querySelector('input[name="csrf"]');
      if (!token) { return; }
      all('input[name="csrf"]').forEach(function (field) { field.value = token.value; });
    }

    return { onThisSite: onThisSite, eligible: eligible, send: send, read: read, copyToken: copyToken };
  })();

  // Put a group from another rendering of this tab in place of group, and
  // return it.
  function replaceGroup(group, fresh) {
    var node = document.importNode(fresh, true);
    group.parentNode.replaceChild(node, group);
    all('[data-clone]', node).forEach(cloneField);
    selectOnFocus(node);
    syncGroup(node);
    return node;
  }

  function parsePage(html) {
    return new DOMParser().parseFromString(html, 'text/html');
  }

  // Take from doc, another rendering of this tab, the named group and every
  // group without a change, so they show what is saved now. A group with a
  // change keeps it. Returns the named group.
  function takeGroups(doc, name) {
    var taken = null;
    all('[data-group]', settingsPanel).forEach(function (group) {
      var own = group.getAttribute('data-group');
      var fresh = doc.querySelector('[data-group="' + own + '"]');
      if (!fresh || (own !== name && groupDirty(group))) { return; }
      var node = replaceGroup(group, fresh);
      if (own === name) { taken = node; }
    });
    // A saved change can end a session, the one whose token the forms
    // carry included, or change how this page is reached. The sidebar's
    // session controls and the connection label say so.
    groupSave.copyToken(doc);
    takeChrome(doc, '[data-session-controls]', document.querySelector('.sidebar__inner'));
    takeChrome(doc, '[data-connection]', null);
    return taken;
  }

  // Put the part of doc that selector finds in place of this page's, or
  // remove this page's when doc has none, or add doc's to parent when this
  // page has none.
  function takeChrome(doc, selector, parent) {
    var own = document.querySelector(selector);
    var fresh = doc.querySelector(selector);
    if (own && fresh) {
      own.parentNode.replaceChild(document.importNode(fresh, true), own);
    } else if (own) {
      own.parentNode.removeChild(own);
    } else if (fresh && parent) {
      parent.appendChild(document.importNode(fresh, true));
    }
  }

  function showSettingsNote(group, selector) {
    var template = settingsPanel.querySelector(selector);
    if (!template) { return; }
    all('[data-settings-note]', group).forEach(function (note) { note.parentNode.removeChild(note); });
    var note = template.cloneNode(true);
    note.removeAttribute(selector.slice(1, -1));
    note.setAttribute('data-settings-note', '');
    note.hidden = false;
    var bar = group.querySelector('[data-group-bar]');
    if (bar) { bar.parentNode.insertBefore(note, bar); } else { group.appendChild(note); }
    note.focus();
  }

  function samePage(target) {
    return target.pathname === window.location.pathname;
  }

  function focusFirst(group, selectors) {
    for (var i = 0; i < selectors.length; i++) {
      var target = group && group.querySelector(selectors[i]);
      if (target) { target.focus(); return; }
    }
  }

  // Let the browser submit the form as a whole page, as without the
  // script. The server answers it completely on its own.
  function submitPage(group) {
    group.removeAttribute('aria-busy');
    HTMLFormElement.prototype.submit.call(groupForm(group));
    return false;
  }

  function saveGroup(group) {
    var form = groupForm(group);
    var name = group.getAttribute('data-group');
    // A form this script does not send is submitted by the browser, which
    // checks its fields first.
    if (!groupSave || !groupSave.eligible(form)) {
      if (form.requestSubmit) { form.requestSubmit(); } else { submitPage(group); }
      return Promise.resolve(false);
    }
    group.setAttribute('aria-busy', 'true');
    return groupSave.send(form, name).then(function (response) {
      var type = response.headers.get('Content-Type') || '';
      if (response.status === 200 && type.indexOf('application/json') === 0) {
        return response.json().then(function (answer) { return showSaved(group, name, answer && answer.location); });
      }
      // Anything but a saved change or this tab with the group showing why
      // nothing was saved, such as a redirect to sign in, is left to the
      // page submission.
      if (response.type === 'opaqueredirect' || type.indexOf('text/html') !== 0) { return submitPage(group); }
      return response.text().then(function (html) {
        var fresh = parsePage(html).querySelector('[data-group="' + name + '"]');
        if (!fresh) { return submitPage(group); }
        var node = replaceGroup(group, fresh);
        focusFirst(node, ['[aria-invalid="true"]', '[role="alert"]', '[data-group-note]', '[data-group-save]']);
        return false;
      });
    }, function () {
      // Not sent: the page submission sends it.
      return submitPage(group);
    });
  }

  // The change is saved. Show it from the address that the server named, or
  // go there when it is not this tab. An address on another site is not
  // followed.
  function showSaved(group, name, location) {
    var target = typeof location === 'string' && location !== '' ? groupSave.onThisSite(location) : null;
    if (!target) {
      group.removeAttribute('aria-busy');
      showSettingsNote(group, '[data-settings-unexpected]');
      return true;
    }
    if (!samePage(target)) {
      window.location.assign(target.href);
      return true;
    }
    return groupSave.read(target.href).then(function (html) {
      var node = takeGroups(parsePage(html), name);
      if (!node) { throw new Error('settings group'); }
      focusFirst(node, ['[data-group-note]', 'h2']);
      return true;
    }).catch(function () {
      // Saved, but this page could not show it: load the page that does.
      window.location.assign(target.href);
      return true;
    });
  }

  // Focus the group's first control on screen, as the save bar that held
  // focus is gone.
  function focusControl(group) {
    var shown = all('select, textarea, input:not([type="hidden"]), summary', group).filter(function (node) {
      return node.getClientRects().length > 0;
    })[0];
    if (shown) { shown.focus(); }
  }

  if (settingsPanel) {
    var editGroup = function (event) {
      var control = event.target;
      if (control.type === 'password' && event.type === 'input') { control.setAttribute('data-typed', ''); }
      var group = control.closest && control.closest('[data-group]');
      if (group) { syncGroup(group); }
    };
    settingsPanel.addEventListener('input', editGroup);
    settingsPanel.addEventListener('change', editGroup);
    settingsPanel.addEventListener('submit', function (event) {
      var form = event.target;
      var group = form.closest && form.closest('[data-group]');
      if (!group || !form.hasAttribute('data-group-form') || form.hasAttribute('data-group-native') ||
          !groupSave || !groupSave.eligible(form)) { return; }
      event.preventDefault();
      if (group.getAttribute('aria-busy') !== 'true') { saveGroup(group); }
    });
    // Cancel puts the saved values back. A refused group, which shows why,
    // follows its link to the tab as saved.
    settingsPanel.addEventListener('click', function (event) {
      var cancel = event.target.closest && event.target.closest('[data-group-cancel]');
      var group = cancel && cancel.closest('[data-group]');
      if (!group || group.hasAttribute('data-group-refused') ||
          event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) { return; }
      event.preventDefault();
      discardGroup(group);
      focusControl(group);
    });
    all('[data-group]', settingsPanel).forEach(syncGroup);
    // A saved change comes back at an address naming its group. Its notice
    // takes focus, so it is read out and the reader stays at that group.
    // The browser moves to the address's group once the page has loaded,
    // so the notice is focused after that.
    var savedGroup = window.location.hash.indexOf('#grp-') === 0 ? document.getElementById(window.location.hash.slice(1)) : null;
    var savedNote = savedGroup && savedGroup.querySelector('[data-group-note]');
    if (savedNote && !document.querySelector('[autofocus]')) {
      window.addEventListener('load', function () { savedNote.focus(); });
    }
  }

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
