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

  /* Repository tab strip: keep the tab that matters visible inside it.
   *
   * The strip scrolls sideways on a narrow viewport, and a container's scroll
   * position cannot be set in CSS. At 320px the active tab sat past the right
   * edge with scrollLeft 0, so the page gave no sign of where the reader was.
   *
   * Only the strip's scrollLeft moves, so the document does not move and focus
   * does not change. scrollIntoView is avoided because it scrolls every
   * scrollable ancestor. One helper serves the initial render, an in-place
   * language change, a resize, and keyboard focus, so those cannot drift
   * apart. Without this file the tabs are links the reader can scroll by hand. */

  var TAB_PAD = 14; // matches scroll-padding-inline in the stylesheet

  function revealTab(strip, tab) {
    if (!strip || !tab || strip.scrollWidth <= strip.clientWidth) { return; }

    var stripLeft = strip.getBoundingClientRect().left;
    var stripRight = stripLeft + strip.clientWidth;
    var box = tab.getBoundingClientRect();

    /* Scroll only when part of the tab is actually outside, so a tab that is
     * already whole on screen is never nudged. When it does scroll, it lands
     * clear of the edge by the same padding the stylesheet uses.
     *
     * A tab wider than the strip cannot fit either way, so its start is shown
     * and the label reads from its first word. */
    if (box.left < stripLeft || box.width > strip.clientWidth) {
      strip.scrollLeft -= stripLeft + TAB_PAD - box.left;
    } else if (box.right > stripRight) {
      strip.scrollLeft += box.right - (stripRight - TAB_PAD);
    }
  }

  /* The tab to keep visible is whichever one the reader is working with: the
   * focused tab if focus is inside this strip, otherwise the current page. */
  function revealTabOfInterest(strip) {
    var focused = document.activeElement;
    if (focused && focused !== document.body && strip.contains(focused)) {
      revealTab(strip, focused.closest('.rtabs__btn') || focused);
      return;
    }
    revealTab(strip, strip.querySelector('.rtabs__btn[aria-current="page"]'));
  }

  function revealTabsOfInterest() {
    all('.rtabs').forEach(revealTabOfInterest);
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

    /* Switching language rewrites every label, so the tabs change width and
     * the one that was visible can end up outside the strip. No resize fires
     * for this, so the same reveal runs here. */
    revealTabsOfInterest();
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

  /* Tab strips: reveal on load, on keyboard focus, and on resize.
   *
   * Tabbing to a partly visible link does not reliably bring the whole link
   * into view, so a focused tab can sit half outside the strip with no way to
   * read its label. focusin is used because focus does not bubble; the handler
   * is bound to the strip, so no other scroll region is affected. */

  var tabStrips = all('.rtabs');
  if (tabStrips.length) {
    tabStrips.forEach(revealTabOfInterest);

    tabStrips.forEach(function (strip) {
      strip.addEventListener('focusin', function (event) {
        var tab = event.target.closest && event.target.closest('.rtabs__btn');
        if (tab) { revealTab(strip, tab); }
      });
    });

    /* A resize changes how much fits and can push the tab of interest back out
     * of view. A few measurements, so no observer is needed. */
    var resizeQueued = false;
    window.addEventListener('resize', function () {
      if (resizeQueued) { return; }
      resizeQueued = true;
      var run = function () {
        resizeQueued = false;
        revealTabsOfInterest();
      };
      if (window.requestAnimationFrame) {
        window.requestAnimationFrame(run);
      } else {
        window.setTimeout(run, 60);
      }
    });
  }
})();
