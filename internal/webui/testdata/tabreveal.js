/* Exercises the shipped revealTab against geometry measured in a real browser.
 *
 * The static tests check what the script says; this checks what it computes.
 * revealTab reads four numbers off the strip and a rectangle off the tab, so
 * it runs against stubs without a DOM library. The stub reproduces the one
 * behaviour that matters: a tab's on-screen box moves as the strip scrolls,
 * and the browser clamps scrollLeft to the scrollable range.
 *
 * Usage: node tabreveal.js <path to owngit.js>
 * Prints a JSON report and exits non-zero when a case fails.
 */

'use strict';

var fs = require('fs');

function loadRevealTab(path) {
  var source = fs.readFileSync(path, 'utf8');
  var start = source.indexOf('var TAB_PAD');
  var end = source.indexOf('/* The tab to keep visible', start);
  if (start < 0 || end < 0) {
    throw new Error('revealTab was not found in ' + path);
  }
  var factory = new Function(source.slice(start, end) + '\nreturn revealTab;');
  return factory();
}

function makeStrip(left, clientWidth, scrollWidth, scrollLeft) {
  return {
    left: left,
    clientWidth: clientWidth,
    scrollWidth: scrollWidth,
    scrollLeft: scrollLeft,
    getBoundingClientRect: function () { return { left: left }; },
  };
}

/* A tab's box is its position in the strip's content, shifted by how far the
 * strip has scrolled since the measurement was taken. */
function makeTab(strip, contentLeft, contentRight, measuredAtScroll) {
  return {
    getBoundingClientRect: function () {
      var shift = strip.scrollLeft - measuredAtScroll;
      return {
        left: contentLeft - shift,
        right: contentRight - shift,
        width: contentRight - contentLeft,
      };
    },
  };
}

function run(revealTab) {
  var results = [];

  function record(name, passed, detail) {
    results.push(Object.assign({ name: name, ok: passed }, detail || {}));
  }

  function revealAndReport(name, strip, tab) {
    var maxScroll = strip.scrollWidth - strip.clientWidth;
    revealTab(strip, tab);
    strip.scrollLeft = Math.max(0, Math.min(strip.scrollLeft, maxScroll));

    var box = tab.getBoundingClientRect();
    var visibleLeft = strip.left;
    var visibleRight = strip.left + strip.clientWidth;
    var fully = box.left >= visibleLeft - 0.5 && box.right <= visibleRight + 0.5;

    record(name, fully, {
      scrollLeft: Math.round(strip.scrollLeft * 10) / 10,
      left: Math.round(box.left * 10) / 10,
      right: Math.round(box.right * 10) / 10,
      fullyVisible: fully,
    });
    return strip.scrollLeft;
  }

  /* Measured at 320px on the tasks page: strip left 13, clientWidth 279,
   * scrollWidth 386, scrollLeft 0. */
  function narrowStrip() { return makeStrip(13, 279, 386, 0); }

  // The reported defect: the active tab sits past the right edge at load.
  var atLoad = narrowStrip();
  revealAndReport(
    'active tab hidden at load is revealed',
    atLoad,
    makeTab(atLoad, 329.5, 398.5, 0)
  );

  /* Keyboard focus. Native Tab does not reliably bring a partly visible link
   * fully into view, so the strip reveals the tab that actually has focus. */
  var tabbing = narrowStrip();
  revealAndReport(
    'focused tab that is partly outside is revealed',
    tabbing,
    makeTab(tabbing, 228.93, 327.5, 0)
  );
  revealAndReport(
    'focus moving on to the last tab is revealed',
    tabbing,
    makeTab(tabbing, 329.5, 398.5, 0)
  );

  /* A tab overflowing on the right moves the shorter way. Dragging it to the
   * left edge instead would scroll past tabs the reader could still see.
   *
   * The strip here has room to scroll further than either answer needs, so the
   * two are not flattened together by the clamp at the end of the range. */
  var minimal = makeStrip(13, 279, 900, 0);
  revealAndReport('right overflow moves the shorter way', minimal, makeTab(minimal, 329.5, 398.5, 0));
  var shorterWay = 398.5 - (13 + 279 - 14);
  var leftAligned = 329.5 - (13 + 14);
  record('right overflow does not jump to the left edge',
    Math.abs(minimal.scrollLeft - shorterWay) < 1, {
      scrollLeft: Math.round(minimal.scrollLeft * 10) / 10,
      shorterWay: Math.round(shorterWay * 10) / 10,
      leftAligned: Math.round(leftAligned * 10) / 10,
    });

  /* Switching language in place: the Korean 체크 tab ended at 290.35 inside a
   * strip ending at 292, and the English Checks label pushed it to 329.5-398.5
   * with no resize event, so the switch has to ask for the reveal itself. */
  var switching = narrowStrip();
  var beforeSwitch = switching.scrollLeft;
  revealTab(switching, makeTab(switching, 221.35, 290.35, 0));
  record('the Korean label needs no scroll', switching.scrollLeft === beforeSwitch, {
    scrollLeft: switching.scrollLeft,
  });
  revealAndReport(
    'the English label is revealed after the switch',
    switching,
    makeTab(switching, 329.5, 398.5, 0)
  );

  // A tab already in view is left where it is.
  var settled = narrowStrip();
  var before = settled.scrollLeft;
  revealAndReport('tab already in view stays visible', settled, makeTab(settled, 20, 90, 0));
  record('tab already in view is not scrolled', settled.scrollLeft === before, {
    scrollLeft: settled.scrollLeft,
  });

  // Nothing to do when every tab fits.
  var roomy = makeStrip(13, 400, 400, 0);
  var roomyBefore = roomy.scrollLeft;
  revealTab(roomy, makeTab(roomy, 20, 90, 0));
  record('a strip that fits is untouched', roomy.scrollLeft === roomyBefore, {
    scrollLeft: roomy.scrollLeft,
  });

  // Scrolled to the end, then back to a tab off the left edge.
  var back = makeStrip(13, 279, 386, 107);
  revealAndReport('tab off the left edge is revealed', back, makeTab(back, 20, 90, 107));

  /* Revealed with room around it, matching scroll-padding-inline, rather than
   * flush against the edge where it reads as cut off. Measured on a strip with
   * range to spare, since at the end of the range the browser's clamp decides
   * the gap rather than the script. */
  var roomy2 = makeStrip(13, 279, 900, 400);
  // Measured off the left edge at -60..10, so the reveal has to move it.
  revealTab(roomy2, makeTab(roomy2, -60, 10, 400));
  roomy2.scrollLeft = Math.max(0, Math.min(roomy2.scrollLeft, roomy2.scrollWidth - roomy2.clientWidth));
  var gapBox = makeTab(roomy2, -60, 10, 400).getBoundingClientRect();
  record('a revealed tab is not flush against the edge', gapBox.left - roomy2.left >= 13, {
    gap: Math.round((gapBox.left - roomy2.left) * 10) / 10,
    scrollLeft: Math.round(roomy2.scrollLeft * 10) / 10,
  });

  /* A tab wider than the strip cannot be shown whole, so its start is aligned
   * and the label reads from its first word rather than its last. Full
   * visibility is impossible here, which is why this case checks the position
   * instead. */
  var cramped = makeStrip(13, 120, 400, 0);
  revealTab(cramped, makeTab(cramped, 200, 380, 0));
  cramped.scrollLeft = Math.max(0, Math.min(cramped.scrollLeft, cramped.scrollWidth - cramped.clientWidth));
  var startBox = makeTab(cramped, 200, 380, 0).getBoundingClientRect();
  record('an oversized tab shows its start', startBox.left >= cramped.left - 0.5, {
    scrollLeft: Math.round(cramped.scrollLeft * 10) / 10,
    left: Math.round(startBox.left * 10) / 10,
    stripLeft: cramped.left,
  });

  return results;
}

var results;
try {
  results = run(loadRevealTab(process.argv[2]));
} catch (error) {
  console.log(JSON.stringify([{ name: 'harness', ok: false, error: String(error) }]));
  process.exit(1);
}

console.log(JSON.stringify(results, null, 1));
process.exit(results.some(function (r) { return !r.ok; }) ? 1 : 0);
