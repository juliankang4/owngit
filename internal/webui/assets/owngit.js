/* OwnGit dashboard behaviour.
 *
 * Every screen works without this file: links navigate, forms submit, and the
 * server renders the chosen language. The script only improves what a reload
 * would otherwise cost, and never invents text of its own.
 *
 * It provides these enhancements:
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
 *      what was typed in them. Before leaving a page whose groups hold a
 *      change, ask whether to save it, discard it or stay.
 *
 *   8. Setup folders: browse folders on the OwnGit host and fill the editable
 *      storage path, without changing setup submission or its validation.
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

  function preferenceCookieName(base) {
    return root.hasAttribute('data-secure') ? '__Host-' + base : base + '_http';
  }

  // Preference cookies only. These are not credentials, so they carry no
  // authentication value and are readable by the page on purpose.
  function writeCookie(name, value) {
    var parts = [
      encodeURIComponent(preferenceCookieName(name)) + '=' + encodeURIComponent(value),
      'Path=/',
      'Max-Age=31536000',
      'SameSite=Lax'
    ];
    if (root.hasAttribute('data-secure')) { parts.push('Secure'); }
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
   * is still in this browser's storage and is read once, then moved. Older
   * cookie names are also read until this address has its own choice. */

  var APPEARANCE_KEY = 'owngit_appearance';

  function validAppearance(value) {
    return value === 'light' || value === 'dark' || value === 'system';
  }

  function currentAppearance() {
    var stored = readCookie(preferenceCookieName(APPEARANCE_KEY));
    if (validAppearance(stored)) { return stored; }
    stored = readCookie(APPEARANCE_KEY);
    if (validAppearance(stored)) { return stored; }
    try { stored = window.localStorage.getItem(APPEARANCE_KEY) || ''; } catch (e) { stored = ''; }
    return stored === 'light' || stored === 'dark' ? stored : 'system';
  }

  function applyAppearance(choice) {
    root.classList.toggle('theme-light', choice === 'light');
    root.classList.toggle('theme-dark', choice === 'dark');
    root.classList.toggle('theme-system', choice === 'system');
    root.setAttribute('data-appearance', choice);
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
  if (!validAppearance(readCookie(preferenceCookieName(APPEARANCE_KEY))) &&
      (validAppearance(readCookie(APPEARANCE_KEY)) || initialAppearance !== 'system')) {
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
    var cells = all('[data-day]', graph).sort(function (a, b) {
      return Number(a.getAttribute('data-index')) - Number(b.getAttribute('data-index'));
    });
    if (!cells.length) { return; }

    var readout = graph.querySelector('[data-graph-readout]');
    var focused = null;

    cells.forEach(function (cell) {
      cell.setAttribute('tabindex', '-1');
    });

    var elapsed = cells.filter(function (cell) { return cell.getAttribute('data-future') !== 'true'; });
    var initial = graph.querySelector('[data-day][aria-current="true"]') || elapsed[elapsed.length - 1] || cells[0];
    initial.setAttribute('tabindex', '0');
    focused = initial;

    var scroll = graph.querySelector('.hm__scroll');
    if (scroll && scroll.scrollWidth > scroll.clientWidth) {
      var viewport = scroll.getBoundingClientRect();
      var day = initial.getBoundingClientRect();
      if (day.left < viewport.left || day.right > viewport.right) {
        scroll.scrollLeft += (day.left + day.right - viewport.left - viewport.right) / 2;
      }
    }

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

  /* Folder navigation uses the setup session and its anti-forgery token.
   * Only an explicit choice fills the field. The normal setup form still
   * submits and validates the path, including when scripting is absent. */
  (function folderChooser() {
    var dialog = document.querySelector('[data-folder-chooser]');
    var opener = document.querySelector('[data-folder-open]');
    var field = document.querySelector('#storage_path');
    if (!dialog || !opener || !field || !dialog.showModal || !window.fetch || !window.AbortController) { return; }
    var list = dialog.querySelector('[data-folder-list]');
    var pathLabel = dialog.querySelector('[data-folder-path]');
    var drives = dialog.querySelector('[data-folder-drives]');
    var parent = dialog.querySelector('[data-folder-parent]');
    var hidden = dialog.querySelector('[data-folder-hidden]');
    var use = dialog.querySelector('[data-folder-use]');
    var create = dialog.querySelector('[data-folder-create]');
    var createButton = dialog.querySelector('[data-folder-create-button]');
    var name = document.querySelector('#folder-name');
    var retry = dialog.querySelector('[data-folder-retry]');
    var limit = dialog.querySelector('[data-folder-limit]');
    var skipped = dialog.querySelector('[data-folder-skipped]');
    var current = '', parentPath = '', roots = false, parentRoots = false;
    var ready = false, busy = false, generation = 0, controller = null;

    function say(code) {
      var found = false;
      all('[data-folder-message]', dialog).forEach(function (node) {
        node.hidden = node.getAttribute('data-folder-message') !== code;
        if (!node.hidden) { found = true; }
      });
      if (code && !found) { say('folder.failed'); }
    }

    function setBusy(on) {
      busy = on;
      dialog.setAttribute('aria-busy', on ? 'true' : 'false');
      parent.disabled = on || (!parentPath && !parentRoots);
      hidden.disabled = on;
      use.disabled = on || !ready || !current;
      createButton.disabled = on || !ready || !current;
      name.disabled = on || !ready || !current;
      retry.disabled = on;
      all('button', list).forEach(function (button) { button.disabled = on; });
    }

    function locationOf(result, showRoots) {
      current = result.path || '';
      parentPath = result.parent || '';
      parentRoots = result.parent_roots === true;
      pathLabel.textContent = current;
      pathLabel.hidden = showRoots;
      drives.hidden = !showRoots;
      roots = showRoots;
    }

    function request(address, values, creating) {
      var target;
      try { target = new URL(address, window.location.href); } catch (e) { return Promise.reject(e); }
      var expected = creating ? '/setup/folders/new' : '/setup/folders';
      if (target.origin !== window.location.origin || target.pathname !== expected || target.search || target.hash) { return Promise.reject(); }
      var token = field.form.querySelector('input[name="csrf"]');
      if (!token) { return Promise.reject(); }
      var body = new URLSearchParams();
      body.set('csrf', token.value);
      body.set('path', values.path);
      body.set('hidden', hidden.checked ? '1' : '');
      body.set('roots', values.roots ? '1' : '');
      body.set('start', values.start ? '1' : '');
      if (creating) { body.set('name', values.name); }
      controller = new AbortController();
      var active = controller;
      var timer = window.setTimeout(function () { active.abort(); }, 5000);
      return fetch(target.href, {
        method: 'POST', credentials: 'same-origin', redirect: 'manual',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'Accept': 'application/json' },
        body: body.toString(), signal: active.signal
      }).then(function (response) {
        if (!(response.headers.get('Content-Type') || '').includes('application/json')) { throw new Error(); }
        return response.json().then(function (result) {
          if (!response.ok && !result.error) { throw new Error(); }
          return result;
        });
      }).finally(function () {
        window.clearTimeout(timer);
        if (controller === active) { controller = null; }
      });
    }

    function load(path, showRoots, focusList, created, start) {
      var focused = document.activeElement;
      var ticket = ++generation;
      ready = false;
      locationOf({ path: path }, showRoots);
      list.replaceChildren();
      list.hidden = false;
      limit.hidden = true;
      skipped.hidden = true;
      retry.hidden = true;
      say('folder.loading');
      setBusy(true);
      return request(dialog.getAttribute('data-list-url'), { path: path, roots: showRoots, start: start }, false).then(function (result) {
        if (ticket !== generation || !dialog.open) { return; }
        if (result.error) {
          if (result.path) { locationOf(result, showRoots); }
          say(result.error);
          list.hidden = true;
          retry.hidden = false;
          return;
        }
        if (!Array.isArray(result.folders)) { throw new Error(); }
        locationOf(result, showRoots);
        ready = true;
        limit.hidden = !result.truncated;
        skipped.hidden = !result.skipped_folders;
        if (start && result.suggested_name) { name.value = result.suggested_name; }
        result.folders.forEach(function (folder) {
          var row = document.createElement('li');
          var button = document.createElement('button');
          var label = document.createElement('span');
          button.type = 'button';
          button.className = 'folderchooser__folder';
          label.dir = 'auto';
          label.textContent = folder.name;
          button.appendChild(label);
          button.addEventListener('click', function () { if (!busy) { load(folder.path, false, true, false); } });
          row.appendChild(button);
          list.appendChild(row);
        });
        say(created ? 'folder.created' : (result.started_at_parent ? 'folder.parent_opened' :
          (!result.folders.length && !result.truncated && !result.skipped_folders ? 'folder.empty' : '')));
        if (focusList) {
          setBusy(false);
          var first = list.querySelector('button');
          (first || (current ? use : parent)).focus();
        }
      }).catch(function () {
        if (ticket !== generation || !dialog.open) { return; }
        say('folder.failed');
        list.hidden = true;
        retry.hidden = false;
      }).finally(function () {
        if (ticket !== generation || !dialog.open) { return; }
        setBusy(false);
        if (focusList && !ready) {
          (parent.disabled ? retry : parent).focus();
        } else if (!focusList && dialog.contains(focused) && !focused.disabled &&
                   (document.activeElement === document.body || document.activeElement === dialog)) {
          focused.focus();
        }
      });
    }

    opener.hidden = false;
    opener.addEventListener('click', function () {
      if (dialog.open) { return; }
      if (controller) { controller.abort(); }
      current = field.value.trim() || field.getAttribute('placeholder') || '';
      parentPath = '';
      parentRoots = false;
      roots = false;
      name.value = '';
      pathLabel.textContent = current;
      drives.hidden = true;
      dialog.showModal();
      load(current, false, false, false, true);
    });
    dialog.addEventListener('close', function () {
      // Native close events are queued. A reopened dialog owns its new request.
      if (dialog.open) { return; }
      generation++;
      if (controller) { controller.abort(); }
      opener.focus();
    });
    dialog.querySelector('[data-folder-cancel]').addEventListener('click', function () { dialog.close(); });
    parent.addEventListener('click', function () { load(parentPath, parentRoots, true, false); });
    hidden.addEventListener('change', function () { load(current, roots, false, false); });
    retry.addEventListener('click', function () { load(current, roots, true, false); });
    use.addEventListener('click', function () {
      if (!ready || busy || !current) { return; }
      field.value = current;
      field.dispatchEvent(new Event('input', { bubbles: true }));
      field.dispatchEvent(new Event('change', { bubbles: true }));
      dialog.close();
    });
    create.addEventListener('submit', function (event) {
      event.preventDefault();
      if (!ready || busy || !current) { return; }
      var ticket = ++generation;
      say('folder.loading');
      setBusy(true);
      request(dialog.getAttribute('data-create-url'), { path: current, name: name.value }, true).then(function (result) {
        if (ticket !== generation || !dialog.open) { return; }
        if (result.error) {
          say(result.error);
          if (result.error === 'folder.create_unconfirmed') { ready = false; retry.hidden = false; }
          setBusy(false);
          (ready ? name : retry).focus();
          return;
        }
        name.value = '';
        load(result.path, false, true, true);
      }).catch(function () {
        if (ticket !== generation || !dialog.open) { return; }
        ready = false;
        say('folder.create_unconfirmed');
        retry.hidden = false;
        setBusy(false);
        retry.focus();
      });
    });
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

  /* Import source: connection choices belong to one address.
   *
   * When the address is edited, the plain HTTP, redirect and exceptional
   * destination choices drawn for the saved address are cleared, and the
   * owner is told. A choice made after that is for the address then in the
   * field, which options_url records; the server ignores choices recorded
   * for another address, so without scripting a changed address is saved
   * with the choices reset. */

  all('[data-import-options-url]').forEach(function (forURL) {
    var form = forURL.form;
    var address = form && form.querySelector('[data-import-url]');
    if (!address) { return; }
    var note = form.querySelector('[data-import-transport-reset]');
    var controls = all('[data-import-transport]', form);
    address.addEventListener('input', function () {
      if (address.value.trim() === forURL.value) { return; }
      var changed = false;
      controls.forEach(function (control) {
        if (control.type === 'checkbox' && control.checked) { control.checked = false; changed = true; }
        if (control.type === 'radio' && control.checked !== (control.value === 'refuse')) {
          control.checked = control.value === 'refuse'; changed = true;
        }
        if (control.type !== 'checkbox' && control.type !== 'radio' && control.value !== '') { control.value = ''; changed = true; }
      });
      if (changed && note) { note.textContent = note.getAttribute('data-import-transport-reset'); }
    });
    function chosen() { forURL.value = address.value.trim(); }
    controls.forEach(function (control) {
      control.addEventListener('input', chosen);
      control.addEventListener('change', chosen);
    });
  });

  /* Name the registry a missing image is downloaded from as the image
   * field is edited. The rule is state.ContainerImageRegistry's: the first
   * path part names the registry when it holds "." or ":" or is
   * "localhost"; otherwise the registry is docker.io. The words come from
   * the server in both languages. */

  all('[data-pull-registry]').forEach(function (note) {
    var form = note.closest('form');
    var image = form && form.querySelector('[name="container_image"]');
    if (!image) { return; }
    function registry(value) {
      var name = value.split('@')[0];
      var slash = name.indexOf('/');
      var first = slash < 0 ? '' : name.slice(0, slash);
      return slash >= 0 && (/[.:]/.test(first) || first === 'localhost') ? first : 'docker.io';
    }
    function update() {
      var value = image.value.trim();
      note.hidden = value === '';
      if (value === '') { return; }
      var name = registry(value);
      var en = (note.getAttribute('data-registry-en') || '').replace('%s', name);
      var ko = (note.getAttribute('data-registry-ko') || '').replace('%s', name);
      note.setAttribute('data-en', en);
      note.setAttribute('data-ko', ko);
      note.textContent = root.getAttribute('data-lang') === 'ko' ? ko : en;
    }
    image.addEventListener('input', update);
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

  /* Repository filter. It only hides rows already on the page, so nothing is
   * written into the field or the list while the reader types, which keeps
   * an input method's composition intact. */

  var sideFilter = document.querySelector('[data-sb-filter]');
  if (sideFilter) {
    var sideRows = all('[data-sb-name]');
    var noMatch = document.querySelector('[data-sb-nomatch]');
    sideFilter.hidden = false;
    sideFilter.addEventListener('input', function () {
      var wanted = sideFilter.value.trim().normalize('NFC').toLowerCase();
      var shown = 0;
      sideRows.forEach(function (row) {
        var hit = !wanted || row.getAttribute('data-sb-name').normalize('NFC').toLowerCase().indexOf(wanted) !== -1;
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

  /* A fragment is not sent to the server. Route an out-of-page line to
   * its pinned, script-free line address without accumulating page content. */
  function routeCodeLine() {
    var panel = document.querySelector('[data-line-page]');
    var match = /^#L([1-9][0-9]*)$/.exec(window.location.hash);
    if (!panel || !match) { return; }
    var line = Number(match[1]);
    if (!Number.isSafeInteger(line) || document.getElementById('L' + line)) { return; }
    var address = new URL(panel.getAttribute('data-line-page'), window.location.href);
    address.searchParams.set('line', String(line));
    address.hash = window.location.hash;
    window.location.replace(address.href);
  }
  if (document.querySelector('[data-line-page]')) {
    routeCodeLine();
    window.addEventListener('hashchange', routeCodeLine);
  }

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

  /* Settings groups. settings.html describes the markup.
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
    return !!form && (group.hasAttribute('data-group-attempt') || settingControls(form).some(function (control) {
      return currentValue(control) !== savedValue(control);
    }));
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
    var showDirty = dirty && (!group.querySelector('[data-settings-note]') || group.getAttribute('data-group-attempt') === 'edited');
    all('[data-group-mark]', group).forEach(function (node) { node.hidden = !showDirty; });
    all('[data-group-bar-title]', group).forEach(function (node) { node.hidden = !showDirty; });
    all('[data-group-bar]', group).forEach(function (node) { node.hidden = !open; });
    syncLeave(false);
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
    editSettingsDraft(group);
    syncGroup(group);
  }

  /* groupSave is the Settings request block, alongside the approval watcher and folder chooser,
   * that sends a request, and a security review checks it on its own
   * (asset_pins_test.go pins it). It sends a group's form only when the
   * form has no password field, so it never builds the form data of a form
   * that holds a password. It POSTs to the form's own address on this
   * site, and after a saved change makes one GET of the address the server
   * returned, again only on this site. The same GET reads this tab again
   * while its backup state shows work in progress. Both send only this
   * site's cookies and follow no redirect. It copies the anti-forgery
   * token of the fetched page into this page's forms, because a saved
   * change can end the session that token belongs to. It stores nothing. */
  var groupSave = (function groupSave() {
    if (!window.fetch || !window.AbortController || !window.DOMParser || !window.FormData || !window.URLSearchParams) { return null; }

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

    function receiveWithin30Seconds(start) {
      var controller = new AbortController();
      var timer = window.setTimeout(function () { controller.abort(); }, 30000);
      return Promise.resolve().then(function () { return start(controller.signal); }).then(function (response) {
        return response.text().then(function (html) { return { response: response, html: html }; });
      }).finally(function () { window.clearTimeout(timer); });
    }

    function send(form, name) {
      var target = onThisSite(formAddress(form));
      if (holdsPassword(form) || !target) { return Promise.reject(new Error('not sent')); }
      return receiveWithin30Seconds(function (signal) {
        return window.fetch(target.href, {
          method: 'POST',
          body: new URLSearchParams(new FormData(form)),
          credentials: 'same-origin',
          cache: 'no-store',
          redirect: 'manual',
          signal: signal,
          headers: { 'X-OwnGit-Group': name }
        });
      });
    }

    function read(address) {
      var target = onThisSite(address);
      if (!target) { return Promise.reject(new Error('not read')); }
      return receiveWithin30Seconds(function (signal) {
        return window.fetch(target.href, { method: 'GET', credentials: 'same-origin', cache: 'no-store', redirect: 'manual', signal: signal });
      }).then(function (result) {
        if (result.response.status !== 200) { throw new Error('not read'); }
        return result.html;
      });
    }

    function copyToken(doc) {
      var token = doc.querySelector('input[name="csrf"]');
      if (!token) { return; }
      all('input[name="csrf"]').forEach(function (field) { field.value = token.value; });
    }

    return { onThisSite: onThisSite, eligible: eligible, send: send, read: read, copyToken: copyToken };
  })();

  function replaceGroup(group, fresh) {
    var node = document.importNode(fresh, true);
    group.parentNode.replaceChild(node, group);
    prepareGroup(node);
    return node;
  }

  function prepareGroup(node) {
    all('[data-clone]', node).forEach(cloneField);
    selectOnFocus(node);
    syncGroup(node);
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
    restartBackupWatch();
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

  function clearSettingsNotes(group) {
    all('[data-settings-note], [data-group-note].notice--success, [data-group-note].notice--warning', group).forEach(function (note) { note.parentNode.removeChild(note); });
  }

  function editSettingsDraft(group) {
    if (group.getAttribute('data-group-attempt') === 'edited') { return; }
    clearSettingsNotes(group);
    if (!group.hasAttribute('data-group-attempt')) { return; }
    group.setAttribute('data-group-attempt', 'edited');
    var outcome = group.getAttribute('data-group-outcome');
    if (outcome) { showSettingsNote(group, outcome, false); }
  }

  function showSettingsNote(group, selector, focus) {
    if (group.getAttribute('data-group-attempt') === 'edited') {
      selector = selector === '[data-settings-refresh-failed]' ? '[data-settings-edited-saved]' : '[data-settings-edited-unconfirmed]';
    }
    var template = settingsPanel.querySelector(selector);
    if (!template) { return; }
    clearSettingsNotes(group);
    var note = template.cloneNode(true);
    note.removeAttribute(selector.slice(1, -1));
    note.setAttribute('data-settings-note', '');
    note.hidden = false;
    var bar = group.querySelector('[data-group-bar]');
    if (bar) { bar.parentNode.insertBefore(note, bar); } else { group.appendChild(note); }
    syncGroup(group);
    if (focus !== false) { note.focus(); }
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
    var form = groupForm(group);
    replacePage(group, function () { HTMLFormElement.prototype.submit.call(form); });
    return false;
  }

  // The page is replaced after a save of group: its own change is not
  // lost, so only another group's change makes the browser ask first.
  function replacePage(group, next) {
    if (!dirtyGroups(group).length) { leaveAllowed = true; }
    unguard(next);
  }

  function notSaved(group, selector) {
    group.removeAttribute('aria-busy');
    group.setAttribute('data-group-outcome', selector);
    showSettingsNote(group, selector);
    return false;
  }

  // A form that can be saved without leaving the page.
  function sentByScript(group) {
    var form = groupForm(group);
    return !!form && !!groupSave && groupSave.eligible(form) && !form.hasAttribute('data-group-native');
  }

  function saveGroup(group, stay) {
    if (group.getAttribute('aria-busy') === 'true') { return Promise.resolve(false); }
    var form = groupForm(group);
    var name = group.getAttribute('data-group');
    // A form this script does not send is submitted by the browser, which
    // checks its fields first.
    if (!groupSave || !groupSave.eligible(form)) {
      if (stay) { return Promise.resolve(false); }
      if (form.requestSubmit) { form.requestSubmit(); } else { submitPage(group); }
      return Promise.resolve(false);
    }
    group.setAttribute('aria-busy', 'true');
    group.setAttribute('data-group-attempt', 'current');
    group.removeAttribute('data-group-outcome');
    clearSettingsNotes(group);
    return groupSave.send(form, name).then(function (result) {
      var response = result.response;
      var type = response.headers.get('Content-Type') || '';
      if (response.status === 200 && type.indexOf('application/json') === 0) {
        var answer = JSON.parse(result.html);
        return showSaved(group, name, answer && answer.location);
      }
      if (response.type === 'opaqueredirect' || type.indexOf('text/html') !== 0) {
        return notSaved(group, '[data-settings-unexpected]');
      }
      var fresh = parsePage(result.html).querySelector('[data-group="' + name + '"]');
      if (!fresh || group.getAttribute('data-group-attempt') === 'edited') { return notSaved(group, '[data-settings-unexpected]'); }
      var node = replaceGroup(group, fresh);
      focusFirst(node, ['[aria-invalid="true"]', '[role="alert"]', '[data-group-note]', '[data-group-save]']);
      return false;
    }).catch(function () {
      return notSaved(group, '[data-settings-failed]');
    });
  }

  // The change is saved. Show it from the address that the server named, or
  // go there when it is not this tab. An address on another site is not
  // followed.
  function showSaved(group, name, location) {
    var target = typeof location === 'string' && location !== '' ? groupSave.onThisSite(location) : null;
    if (!target) { return notSaved(group, '[data-settings-unexpected]'); }
    if (group.getAttribute('data-group-attempt') === 'edited') {
      return notSaved(group, '[data-settings-refresh-failed]');
    }
    if (!samePage(target)) {
      replacePage(group, function () { window.location.assign(target.href); });
      return true;
    }
    return groupSave.read(target.href).then(function (html) {
      if (group.getAttribute('data-group-attempt') === 'edited') { return notSaved(group, '[data-settings-refresh-failed]'); }
      var node = takeGroups(parsePage(html), name);
      if (!node) { throw new Error('settings group'); }
      focusFirst(node, ['[data-group-note]', 'h2']);
      return true;
    }).catch(function () {
      return notSaved(group, '[data-settings-refresh-failed]');
    });
  }

  var backupTimer = 0;
  var backupUntil = 0;
  var backupSectionsBehind = false;

  function restartBackupWatch() {
    backupUntil = 0;
    watchBackups();
  }

  function watchBackups() {
    var group = groupSave && groupNamed('backup_runs');
    if (!group || !(group.hasAttribute('data-backup-busy') || backupSectionsBehind)) { backupUntil = 0; return; }
    if (!backupUntil) { backupUntil = Date.now() + 30 * 60 * 1000; }
    if (backupTimer || document.hidden || Date.now() >= backupUntil) { return; }
    backupTimer = window.setTimeout(readBackups, 5000);
  }

  function readBackups() {
    backupTimer = 0;
    var group = groupNamed('backup_runs');
    if (!group || document.hidden) { return; }
    groupSave.read(addressWithoutNotice()).then(function (html) {
      var doc = parsePage(html);
      var replacedMeanwhile = groupNamed('backup_runs') !== group;
      if (replacedMeanwhile) { return; }
      groupSave.copyToken(doc);
      var listCaughtUp = catchUpBackupSection(doc, 'grp-backup-list');
      var uploadCaughtUp = catchUpBackupSection(doc, 'grp-backup-upload');
      backupSectionsBehind = !(listCaughtUp && uploadCaughtUp);
      takeBackupState(group, doc.querySelector('[data-group="backup_runs"]'));
    }).catch(function () {}).then(function () { watchBackups(); });
  }

  function addressWithoutNotice() {
    var address = new URL(window.location.href);
    address.searchParams.delete('notice');
    return address.href;
  }

  function takeBackupState(group, fresh) {
    var active = document.activeElement;
    if (!fresh || group.querySelector('[data-typed]') || selectionIn(group) ||
        (group.contains(active) && /^(INPUT|SELECT|TEXTAREA|BUTTON)$/.test(active.tagName)) ||
        renderedState(fresh) === renderedState(group)) { return; }
    var ended = group.hasAttribute('data-backup-busy') && !fresh.hasAttribute('data-backup-busy');
    takeKeepingReaderNotices(group, fresh);
    if (ended) { sayBackupsEnded(); }
  }

  function takeKeepingReaderNotices(group, fresh) {
    var notices = readerNotices(group);
    var node = document.importNode(fresh, true);
    readerNotices(node).forEach(function (notice) { notice.parentNode.removeChild(notice); });
    Array.prototype.slice.call(group.childNodes).forEach(function (child) {
      if (notices.indexOf(child) < 0) { group.removeChild(child); }
    });
    group.insertBefore(node.querySelector('.grp__h'), notices[0] || null);
    while (node.firstChild) { group.appendChild(node.firstChild); }
    group.toggleAttribute('data-backup-busy', node.hasAttribute('data-backup-busy'));
    prepareGroup(group);
  }

  function readerNotices(group) {
    return all('[data-group-note]', group);
  }

  function catchUpBackupSection(doc, id) {
    var own = document.getElementById(id);
    var fresh = doc.getElementById(id);
    if (!own || !fresh || renderedState(fresh) === renderedState(own)) { return true; }
    if (readerUses(own)) { return false; }
    replaceGroup(own, fresh);
    return true;
  }

  function readerUses(section) {
    return !!section.querySelector('details[data-reader-toggled], [data-typed]') ||
      section.contains(document.activeElement) || selectionIn(section) ||
      all('input[type="file"]', section).some(function (field) { return field.files && field.files.length > 0; });
  }

  function renderedState(node) {
    var copy = node.cloneNode(true);
    readerNotices(copy).forEach(function (notice) { notice.parentNode.removeChild(notice); });
    var disabled = all('button, input, select, textarea', copy).map(function (control) {
      return control.hasAttribute('disabled') ? '1' : '0';
    }).join('');
    return [copy.hasAttribute('data-backup-busy'), disabled, shownText(copy)].join('|');
  }

  function selectionIn(node) {
    var selection = window.getSelection();
    return !!selection && !selection.isCollapsed && node.contains(selection.anchorNode);
  }

  function shownText(node) {
    return node.textContent.replace(/\s+/g, ' ').trim();
  }

  function sayBackupsEnded() {
    var status = settingsPanel.querySelector('[data-backup-ended]');
    if (!status) { return; }
    var en = status.getAttribute('data-say-en') || '';
    var ko = status.getAttribute('data-say-ko') || '';
    status.setAttribute('data-en', en);
    status.setAttribute('data-ko', ko);
    status.textContent = root.getAttribute('data-lang') === 'ko' ? ko : en;
  }

  // Focus the group's first control on screen, as the save bar that held
  // focus is gone.
  function focusControl(group) {
    var shown = all('select, textarea, input:not([type="hidden"]), summary', group).filter(function (node) {
      return node.getClientRects().length > 0;
    })[0];
    if (shown) { shown.focus(); }
  }

  /* Leaving with unsaved changes.
   *
   * While a Settings group holds a change, following a link, sending a form
   * from elsewhere on the page and the browser's Back open the leave dialog
   * (settings.html) instead. Display choices apply at once and never count
   * as a change. Reloading and closing the tab get the browser's own
   * question, the only one a browser allows there, and only while a group
   * holds a change.
   *
   * Back: the first change adds one history entry for this same address,
   * so Back comes to this page first and opens the dialog. Stay adds the
   * entry again, so every Back asks while something is unsaved, and leaving
   * goes back for real. The entry is taken back before any other way out
   * and once nothing holds a change, so it never stays behind in history.
   *
   * Save and leave saves the groups this script can send, one after another
   * with saveGroup, and stops at the first that is not saved: that group
   * shows why, and every group not saved keeps its change. A saved group no
   * longer holds a change, so trying again never repeats its save. Nothing
   * saves every group at once. A form with a password field is sent by the
   * browser as a whole page, never by this script, so such a group can only
   * be the last step: the dialog's own password field and the address being
   * left for are attached to that form, and the server goes there once the
   * change is saved. This part never reads a password field's value. */

  var leaveDialog = settingsPanel && document.querySelector('[data-leave]');
  var leaveReady = !!leaveDialog && typeof leaveDialog.showModal === 'function' &&
    !!window.history && typeof window.history.pushState === 'function';
  var LEAVE_MARK = 'owngitLeaveGuard';
  var leaveArmed = false;       // the extra history entry is the current one
  var leaveUnguarding = false;  // going back from that entry
  var leaveNext = null;         // what follows once the entry is gone
  var leaveTimer = 0;
  var leaveAllowed = false;     // this page is leaving on purpose
  var leaveListening = false;   // the browser's own question is on
  var leaving = null;           // the way out that the dialog holds
  var leaveReturn = null;       // what had focus before the dialog opened
  // Whether Back from here reaches another page of this site. Only then is
  // an entry added for the dialog; without one (Settings opened in a new
  // tab or window) it would only turn on a Back button that goes nowhere.
  // Back to another site unloads this page, so the browser's own question
  // on leaving covers it. The Navigation API lists only this site's
  // entries, so canGoBack answers exactly that; the history length, which
  // also counts other sites, is used only without the API.
  var leaveHasPast = window.navigation && typeof window.navigation.canGoBack === 'boolean' ?
    window.navigation.canGoBack : window.history.length > 1;

  function dirtyGroups(except) {
    return settingsPanel ? all('[data-group]', settingsPanel).filter(function (group) {
      return group !== except && groupDirty(group);
    }) : [];
  }

  function groupNamed(name) {
    return settingsPanel.querySelector('[data-group="' + name + '"]');
  }

  function askBeforeUnload(event) {
    if (leaveAllowed || !dirtyGroups().length) { return; }
    event.preventDefault();
    event.returnValue = '';
  }

  // Follow what the groups hold now. byUser is true right after the owner
  // changed something, the only moment a history entry may be added.
  function syncLeave(byUser) {
    var dirty = dirtyGroups().length > 0;
    if (dirty !== leaveListening) {
      leaveListening = dirty;
      if (dirty) {
        window.addEventListener('beforeunload', askBeforeUnload);
      } else {
        window.removeEventListener('beforeunload', askBeforeUnload);
      }
    }
    if (!leaveReady || leaveAllowed || leaving) { return; }
    if (!dirty) {
      unguard(null);
    } else if (byUser) {
      arm();
    }
  }

  function isGuard(state) {
    return !!state && typeof state === 'object' && state[LEAVE_MARK] === true;
  }

  function guardMark() {
    var mark = {};
    mark[LEAVE_MARK] = true;
    return mark;
  }

  // Moving between this entry and the one it copies keeps the page where
  // the reader is.
  function keepScroll(on) {
    try { window.history.scrollRestoration = on ? 'manual' : 'auto'; } catch (e) { /* not supported */ }
  }

  function arm() {
    if (!leaveHasPast || leaveArmed || leaveUnguarding) { return; }
    try {
      keepScroll(true);
      window.history.pushState(guardMark(), '', window.location.href);
      leaveArmed = true;
    } catch (e) { /* the browser's own question still applies */ }
  }

  // Take the extra entry back, then run next.
  function unguard(next) {
    if (leaveUnguarding) {
      if (next) { leaveNext = next; }
      return;
    }
    if (!leaveArmed) {
      if (next) { next(); }
      return;
    }
    leaveArmed = false;
    leaveUnguarding = true;
    leaveNext = next;
    window.history.back();
    // A safety net: should the step back never be reported, the way out
    // still goes on. No browser is known to skip that popstate; 1.5 s is
    // far above the few milliseconds a same-page step takes in Chrome and
    // WebKit, so the net does not race a normal step.
    leaveTimer = window.setTimeout(unguarded, 1500);
  }

  function unguarded() {
    window.clearTimeout(leaveTimer);
    leaveUnguarding = false;
    keepScroll(false);
    var next = leaveNext;
    leaveNext = null;
    if (next) { next(); } else { syncLeave(true); }
  }

  function leaveWord(name) {
    var word = leaveDialog.querySelector('[data-leave-word="' + name + '"]').cloneNode(true);
    word.removeAttribute('data-leave-word');
    word.hidden = false;
    return word;
  }

  function copyChildren(from, to) {
    if (!from) { return to; }
    Array.prototype.forEach.call(from.childNodes, function (node) { to.appendChild(node.cloneNode(true)); });
    all('svg, .sw__state, .check__d', to).forEach(function (node) { node.parentNode.removeChild(node); });
    return to;
  }

  function controlLabel(control) {
    var label = control.labels && control.labels[0];
    var span = document.createElement('span');
    if (!label) {
      span.textContent = control.name;
      return span;
    }
    copyChildren(label.querySelector('.check__t') || label, span);
    // The words of the label, without the space before a removed part.
    if (span.lastChild && span.lastChild.nodeType === 3) { span.lastChild.nodeValue = span.lastChild.nodeValue.replace(/\s+$/, ''); }
    if (span.firstChild && span.firstChild.nodeType === 3) { span.firstChild.nodeValue = span.firstChild.nodeValue.replace(/^\s+/, ''); }
    return span;
  }

  // A value as the page shows it: a switch in words, a choice by its
  // label, and text as typed.
  function shownValue(control, value) {
    if (control.type === 'checkbox') { return leaveWord(value === 'on' ? 'on' : 'off'); }
    var span = document.createElement('span');
    if (control.tagName === 'SELECT') {
      var option = Array.prototype.filter.call(control.options, function (item) { return item.value === value; })[0];
      if (option) {
        ['data-en', 'data-ko'].forEach(function (attr) {
          if (option.hasAttribute(attr)) { span.setAttribute(attr, option.getAttribute(attr)); }
        });
        span.textContent = option.textContent;
        return span;
      }
    }
    if (value === '') { return leaveWord('empty'); }
    span.className = 'mono';
    span.textContent = value;
    return span;
  }

  // One change: its label, the saved value and the new one. A password
  // field only says that something was entered.
  function changeRow(control, unverified) {
    var row = document.createElement('li');
    row.appendChild(controlLabel(control));
    row.appendChild(document.createTextNode(': '));
    if (control.type === 'password') {
      row.appendChild(leaveWord('entered'));
      return row;
    }
    if (!unverified) {
      row.appendChild(shownValue(control, savedValue(control)));
      row.appendChild(document.createTextNode(' \u2192 '));
    }
    row.appendChild(shownValue(control, currentValue(control)));
    return row;
  }

  // The page being left for, as an address on this site, or '' when it is
  // on another site or not known. Back goes to the page that led here.
  function leaveTarget(leave) {
    var address = leave.href || (leave.back ? document.referrer : '');
    var url;
    if (!address) { return ''; }
    try { url = new URL(address, window.location.href); } catch (e) { return ''; }
    return url.origin === window.location.origin ? url.pathname + url.search + url.hash : '';
  }

  // The administrator password field of a group, when it is shown and
  // nothing was typed into it: the dialog asks for it instead.
  function passwordGate(group) {
    return all('input[data-group-gate]', groupForm(group)).filter(function (field) {
      return field.type === 'password' && !field.disabled && !field.closest('[hidden]') &&
        !field.hasAttribute('data-typed');
    })[0] || null;
  }

  // What the dialog shows and what Save and leave would do.
  function planLeave() {
    var groups = dirtyGroups(leaving.except);
    var paged = groups.filter(function (group) { return !sentByScript(group); });
    var target = leaveTarget(leaving);
    var pageLast = paged.length === 1 && !leaving.form && target !== '';
    leaving.groups = groups;
    leaving.page = pageLast ? paged[0] : null;
    leaving.target = target;
    leaving.canSave = paged.length === 0 || pageLast;
    leaving.gate = pageLast ? passwordGate(paged[0]) : null;

    var unverified = groups.some(function (group) { return group.hasAttribute('data-group-attempt'); });
    all('[data-leave-ordinary]', leaveDialog).forEach(function (node) { node.hidden = unverified; });
    all('[data-leave-unverified]', leaveDialog).forEach(function (node) { node.hidden = !unverified; });
    var list = leaveDialog.querySelector('[data-leave-list]');
    while (list.firstChild) { list.removeChild(list.firstChild); }
    groups.forEach(function (group) {
      var item = document.createElement('li');
      item.appendChild(copyChildren(group.querySelector('.grp__h h2'), document.createElement('b')));
      if (!leaving.canSave && paged.indexOf(group) >= 0) { item.appendChild(leaveWord('apart')); }
      var changes = document.createElement('ul');
      var pending = group.hasAttribute('data-group-attempt');
      settingControls(groupForm(group)).forEach(function (control) {
        if (pending || currentValue(control) !== savedValue(control)) { changes.appendChild(changeRow(control, pending)); }
      });
      item.appendChild(changes);
      list.appendChild(item);
    });
    leaveDialog.querySelector('[data-leave-apart]').hidden = leaving.canSave;
    // The reason Save and leave is missing is read with the description.
    leaveDialog.setAttribute('aria-describedby', leaving.canSave ? 'leave-d' : 'leave-d leave-apart');
    leaveDialog.querySelector('[data-leave-save]').hidden = !leaving.canSave;
    leaveDialog.querySelector('[data-leave-password-field]').hidden = !leaving.gate;
  }

  function setLeaveBusy(busy) {
    if (leaving) { leaving.busy = busy; }
    if (busy) { leaveDialog.setAttribute('aria-busy', 'true'); } else { leaveDialog.removeAttribute('aria-busy'); }
    all('button', leaveDialog).forEach(function (button) { button.disabled = busy; });
    var status = leaveDialog.querySelector('[data-leave-status]');
    while (status.firstChild) { status.removeChild(status.firstChild); }
    if (busy) { status.appendChild(leaveWord('saving')); }
  }

  function openLeave(leave) {
    if (!leaveDialog.open) { leaveReturn = document.activeElement; }
    leaving = leave;
    planLeave();
    if (!leaveDialog.open) { leaveDialog.showModal(); }
    leaveDialog.querySelector('[data-leave-stay]').focus();
  }

  // Attach the dialog's password field, when the page group asks for one,
  // and the address being left for to the page group's form.
  function attachPage(leave) {
    var form = groupForm(leave.page);
    if (!form.id) { form.id = 'leave-form-' + leave.page.getAttribute('data-group'); }
    var to = leaveDialog.querySelector('[data-leave-to]');
    to.value = leave.target;
    to.setAttribute('form', form.id);
    if (leave.gate) {
      leave.gate.disabled = true;
      leaveDialog.querySelector('[data-leave-password]').setAttribute('form', form.id);
    }
    return form;
  }

  function detachPage(leave) {
    var to = leaveDialog.querySelector('[data-leave-to]');
    to.removeAttribute('form');
    to.value = '';
    leaveDialog.querySelector('[data-leave-password]').removeAttribute('form');
    if (leave && leave.gate) { leave.gate.disabled = false; }
  }

  function stay() {
    var leave = leaving;
    leaving = null;
    detachPage(leave);
    leaveDialog.querySelector('[data-leave-password]').value = '';
    setLeaveBusy(false);
    if (leaveDialog.open) { leaveDialog.close(); }
    // Back already used the extra entry; the next Back asks again.
    if (dirtyGroups().length) { arm(); }
    var back = leaveReturn;
    leaveReturn = null;
    if (back && back !== document.body && back.isConnected && typeof back.focus === 'function') {
      back.focus();
    } else {
      focusFirst(dirtyGroups()[0], ['[data-group-save]']);
    }
  }

  // Go where the owner was going.
  function proceed(leave) {
    if (leave.back) {
      window.history.back();
    } else if (leave.href) {
      window.location.assign(leave.href);
    } else if (leave.form) {
      // The group taken again from a saved page replaces its old form.
      var form = leave.form;
      if (!form.isConnected && leave.except) {
        var group = groupNamed(leave.except.getAttribute('data-group'));
        form = group && groupForm(group);
      }
      if (form) { HTMLFormElement.prototype.submit.call(form); }
    }
  }

  function finishLeave() {
    var leave = leaving;
    leaving = null;
    leaveReturn = null;
    leaveAllowed = true;
    if (leaveDialog.open) { leaveDialog.close(); }
    unguard(function () { proceed(leave); });
  }

  function discardAndLeave() {
    if (!leaving || leaving.busy) { return; }
    leaveAllowed = true;
    dirtyGroups(leaving.except).forEach(discardGroup);
    finishLeave();
  }

  function stopLeaving(savedSome, failed) {
    var group = failed && groupNamed(failed);
    stay();
    all('[data-leave-partial-shown]', settingsPanel).forEach(function (note) { note.parentNode.removeChild(note); });
    if (savedSome) {
      var note = settingsPanel.querySelector('[data-leave-partial]').cloneNode(true);
      note.removeAttribute('data-leave-partial');
      note.setAttribute('data-leave-partial-shown', '');
      note.hidden = false;
      // Beside the group that stopped it, where the reader is taken.
      var head = group && group.querySelector('.grp__h');
      if (head) { head.parentNode.insertBefore(note, head.nextSibling); } else { settingsPanel.insertBefore(note, settingsPanel.firstChild); }
    }
    focusFirst(group, ['[aria-invalid="true"]', '[role="alert"]', '[data-settings-note]', '[data-group-note]', '[data-group-save]']);
  }

  function saveAndLeave() {
    if (!leaving || leaving.busy) { return; }
    planLeave();
    if (!leaving.canSave) { return; }
    var leave = leaving;
    var scripted = leave.groups.filter(sentByScript);
    var forms = scripted.map(groupForm);
    if (leave.page) { forms.push(attachPage(leave)); }
    // The browser checks each form first, as it does on the group's Save.
    var invalid = null;
    forms.forEach(function (form) {
      Array.prototype.forEach.call(form.elements, function (control) {
        if (!invalid && control.willValidate && !control.checkValidity()) { invalid = control; }
      });
    });
    if (invalid) {
      if (invalid.hasAttribute('data-leave-password')) {
        invalid.reportValidity();
        detachPage(leave);
      } else {
        stay();
        invalid.reportValidity();
      }
      return;
    }
    setLeaveBusy(true);
    var savedSome = false;
    var failed = '';
    var chain = Promise.resolve(true);
    scripted.forEach(function (group) {
      var name = group.getAttribute('data-group');
      chain = chain.then(function (ok) {
        if (!ok) { return false; }
        return saveGroup(groupNamed(name), true).then(function (saved) {
          if (saved) { savedSome = true; } else { failed = name; }
          return saved;
        });
      });
    });
    chain.then(function (ok) {
      if (leaving !== leave) { return; }
      if (!ok) {
        stopLeaving(savedSome, failed);
      } else if (leave.page) {
        // The last step is a page the browser sends; the dialog stays
        // until the next page arrives.
        leaveAllowed = true;
        var form = groupForm(leave.page);
        unguard(function () { HTMLFormElement.prototype.submit.call(form); });
      } else {
        finishLeave();
      }
    }, function () {
      if (leaving === leave) { stopLeaving(savedSome, failed); }
    });
  }

  // A way out: open the dialog when another group would lose its change.
  // except is the group that the link or form itself cancels or sends.
  function leaveBy(event, leave) {
    if (!leaveReady || leaveAllowed || leaving) { return; }
    if (!dirtyGroups(leave.except).length) {
      if (leave.except && groupDirty(leave.except)) { leaveAllowed = true; }
      if (leaveArmed || leaveUnguarding) {
        event.preventDefault();
        leaveAllowed = true;
        unguard(function () { proceed(leave); });
      }
      return;
    }
    event.preventDefault();
    openLeave(leave);
  }

  // The same page with another fragment.
  function sameDocument(url) {
    var here = window.location;
    return url.origin === here.origin && url.pathname === here.pathname && url.search === here.search;
  }

  if (leaveReady) {
    // A page reloaded at the extra entry holds no change any more.
    if (isGuard(window.history.state)) { window.history.replaceState(null, ''); }

    document.addEventListener('click', function (event) {
      if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) { return; }
      var link = event.target.closest && event.target.closest('a[href]');
      if (!link || leaveDialog.contains(link) || link.hasAttribute('download')) { return; }
      var opens = link.getAttribute('target');
      if (opens && opens !== '_self') { return; }
      var url;
      try { url = new URL(link.href, window.location.href); } catch (e) { return; }
      if (!/^https?:$/.test(url.protocol) || (url.hash && sameDocument(url))) { return; }
      leaveBy(event, { href: url.href, except: link.hasAttribute('data-group-cancel') ? link.closest('[data-group]') : null });
    });

    // A form sent by the browser, from a group or from elsewhere on the
    // page. A group this script saves in place never gets here. Any form of
    // a group, such as a replacement it offers beside its own form, sends
    // that group's change, so the group's draft does not hold it back.
    document.addEventListener('submit', function (event) {
      if (event.defaultPrevented) { return; }
      var form = event.target;
      var opens = form.getAttribute('target');
      if (opens && opens !== '_self') { return; }
      leaveBy(event, { form: form, except: form.closest('[data-group]') });
    });

    window.addEventListener('popstate', function () {
      if (isGuard(window.history.state)) {
        // Back from a fragment of this page, or Forward: still guarded.
        if (leaveUnguarding) { window.history.back(); } else { leaveArmed = true; }
        return;
      }
      if (leaveUnguarding) { unguarded(); return; }
      if (!leaveArmed) { return; }
      leaveArmed = false;
      keepScroll(false);
      if (leaveAllowed || (leaving && leaving.busy)) { return; }
      if (!dirtyGroups().length) { window.history.back(); return; }
      openLeave({ back: true, except: null });
    });

    // A fragment followed while guarded is part of the guard, so Back
    // through it stays on this page.
    window.addEventListener('hashchange', function () {
      if (leaveArmed && !isGuard(window.history.state)) { window.history.replaceState(guardMark(), ''); }
    });

    // Back to this page from the next one, as the browser kept it.
    window.addEventListener('pageshow', function (event) {
      if (!event.persisted) { return; }
      leaving = null;
      leaveAllowed = false;
      leaveUnguarding = false;
      leaveNext = null;
      detachPage(null);
      leaveDialog.querySelector('[data-leave-password]').value = '';
      setLeaveBusy(false);
      if (leaveDialog.open) { leaveDialog.close(); }
      leaveArmed = isGuard(window.history.state);
      syncLeave(false);
    });

    leaveDialog.querySelector('[data-leave-save]').addEventListener('click', saveAndLeave);
    leaveDialog.querySelector('[data-leave-discard]').addEventListener('click', discardAndLeave);
    leaveDialog.querySelector('[data-leave-stay]').addEventListener('click', stay);
    leaveDialog.querySelector('[data-leave-password]').addEventListener('keydown', function (event) {
      if (event.key === 'Enter') {
        event.preventDefault();
        saveAndLeave();
      }
    });
    // Escape is Stay, except while saving.
    leaveDialog.addEventListener('cancel', function (event) {
      if (leaving && leaving.busy) { event.preventDefault(); }
    });
    // The close event comes later than close(), when the dialog may have
    // opened again meanwhile.
    leaveDialog.addEventListener('close', function () {
      if (leaving && !leaveDialog.open) { stay(); }
    });
  }

  if (settingsPanel) {
    var editGroup = function (event) {
      var control = event.target;
      if (control.type === 'password' && event.type === 'input') { control.setAttribute('data-typed', ''); }
      var group = control.closest && control.closest('[data-group]');
      if (!group) { return; }
      // A change after a way out that did not happen asks again.
      if (!leaveUnguarding) { leaveAllowed = false; }
      var form = groupForm(group);
      if (form && settingControls(form).indexOf(control) >= 0) { editSettingsDraft(group); }
      syncGroup(group);
      syncLeave(true);
    };
    settingsPanel.addEventListener('input', editGroup);
    settingsPanel.addEventListener('change', editGroup);
    // Marks a note whose open state the reader changed from the server's; the
    // toggle event cannot tell, as it also fires for a note rendered open.
    settingsPanel.addEventListener('click', function (event) {
      var summary = !event.defaultPrevented && event.target.closest && event.target.closest('summary');
      if (summary && summary.parentNode.tagName === 'DETAILS') { summary.parentNode.toggleAttribute('data-reader-toggled'); }
    });
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
    watchBackups();
    document.addEventListener('visibilitychange', function () { watchBackups(); });
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
