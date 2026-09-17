/* OwnGit dashboard behaviour.
 *
 * Every screen works without this file: links navigate, forms submit, and the
 * server renders the chosen language. The script only improves what a reload
 * would otherwise cost, and never invents text of its own.
 *
 * It does four things:
 *   1. Appearance: Light, Dark, or System, remembered per browser.
 *   2. Language: switch in place so typing in a form is not lost.
 *   3. Activity graph: arrow-key movement and a spoken day readout.
 *   4. Setup link: hold the one-time code in memory, clear it from the URL,
 *      and submit it only when the owner presses the start button.
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
   * rule for System is a media query on a different class. */

  var APPEARANCE_KEY = 'owngit_appearance';
  var darkQuery = window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null;

  function currentAppearance() {
    var stored = '';
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
    all('[data-appearance-set]').forEach(function (button) {
      button.setAttribute('aria-pressed', String(button.getAttribute('data-appearance-set') === choice));
    });
  }

  function setAppearance(choice) {
    try { window.localStorage.setItem(APPEARANCE_KEY, choice); } catch (e) { /* private mode */ }
    applyAppearance(choice);
  }

  applyAppearance(currentAppearance());

  document.addEventListener('click', function (event) {
    var button = event.target.closest && event.target.closest('[data-appearance-set]');
    if (!button) { return; }
    setAppearance(button.getAttribute('data-appearance-set'));
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

  function applyLanguage(lang) {
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
    // keeps the language the reader is actually looking at.
    if (window.history && window.history.replaceState) {
      try {
        var url = new URL(window.location.href);
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
    applyLanguage(link.getAttribute('data-lang-set'));
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
})();
