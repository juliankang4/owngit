(function () {
  'use strict';

  function setOpen(sidebar, toggle, open) {
    sidebar.classList.toggle('is-open', open);
    toggle.setAttribute('aria-expanded', String(open));
  }

  document.addEventListener('click', function (event) {
    var toggle = event.target.closest('[data-sidebar-toggle]');
    var sidebar = toggle && toggle.closest('[data-sidebar]');
    if (sidebar) { setOpen(sidebar, toggle, !sidebar.classList.contains('is-open')); }
  });
  document.addEventListener('keydown', function (event) {
    if (event.isComposing || event.keyCode === 229 || event.key !== 'Escape') { return; }
    var sidebar = event.target.closest('[data-sidebar]');
    var toggle = sidebar && sidebar.querySelector('[data-sidebar-toggle]');
    if (!toggle || toggle.offsetParent === null || !sidebar.classList.contains('is-open')) { return; }
    setOpen(sidebar, toggle, false);
    toggle.focus();
  });

  document.documentElement.classList.add('sidebar-ready');
}());
