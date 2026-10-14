/* Hover text for every agtrace page, shared the way the theme is, so the
   listing and the session page cannot drift into two behaviours.

   The browser's own title tooltip is what this replaces. It waits a second
   or more before it shows, never shows on a touch screen, and in use was not
   seen at all: a help cursor over a figure led to nothing. This shows at
   once, on keyboard focus as well as on hover.

   Anything carrying data-tip gets it — one listener for the whole document,
   because the listing puts a tip on a cell of every row. A tip whose text is
   worked out when it shows (a button saying what clicking it will do next) is
   given with attach instead. Loaded in <head>; the box is made on first use. */
(function () {
  "use strict";
  var ID = "agtrace-tip", GAP = 6, EDGE = 8;
  var box = null, current = null, over = null;

  function el() {
    if (!box) {
      box = document.createElement("div");
      box.className = "tip";
      box.id = ID;
      box.setAttribute("role", "tooltip");
      document.body.appendChild(box);
    }
    return box;
  }

  function textOf(node) {
    var f = node.agtraceTip;
    if (typeof f === "function") return f();
    return node.getAttribute("data-tip") || "";
  }

  // The box sits under the text it describes, not under the element: a
  // figure right-aligned in a wide table cell is at the far end of it, and a
  // box starting at the cell's edge started a column away from the number.
  // An element with no text of its own, a bar, is its own anchor.
  function anchor(node) {
    var r = node.getBoundingClientRect();
    try {
      var range = document.createRange();
      range.selectNodeContents(node);
      var t = range.getBoundingClientRect();
      if (t.width > 0 && t.height > 0) return t;
    } catch (e) {}
    return r;
  }

  // Under the anchor, starting where it starts; above it when there is no
  // room below; inside the window either way.
  function place(node) {
    var b = el(), a = anchor(node);
    var vw = document.documentElement.clientWidth, vh = window.innerHeight;
    var tw = b.offsetWidth, th = b.offsetHeight;
    var left = Math.min(Math.max(EDGE, a.left), vw - tw - EDGE);
    var top = a.bottom + GAP;
    if (top + th > vh - EDGE && a.top - GAP - th >= EDGE) top = a.top - GAP - th;
    b.style.left = (Math.max(EDGE, left) + window.scrollX) + "px";
    b.style.top = (top + window.scrollY) + "px";
  }

  function show(node) {
    var text = textOf(node);
    if (!text) return hide();
    current = node;
    var b = el();
    b.textContent = text;
    b.classList.add("on");
    place(node);
  }
  function hide() {
    current = null;
    if (box) box.classList.remove("on");
  }

  function find(t) {
    while (t && t.nodeType === 1) {
      if (t.hasAttribute("data-tip")) return t;
      t = t.parentElement;
    }
    return null;
  }

  // A keys-only tip shows on focus the keyboard gave, and not on the focus a
  // mouse click gives a button.
  function byKeys(n) {
    try { return n.matches(":focus-visible"); } catch (e) { return true; }
  }

  document.addEventListener("mouseover", function (e) {
    var n = find(e.target);
    if (n && n.agtraceTipKeys) n = null;
    over = n;
    if (n === current) return;
    if (n) show(n);
    else hide();
  });
  // Off the page altogether: no mouseover follows to say so.
  document.addEventListener("mouseout", function (e) {
    if (!e.relatedTarget) { over = null; hide(); }
  });
  document.addEventListener("focusin", function (e) {
    var n = find(e.target);
    if (n && (!n.agtraceTipKeys || byKeys(n))) show(n);
  });
  document.addEventListener("focusout", hide);
  // After a click the tip under the pointer shows again, read afresh: a tip
  // worked out when it shows can say something new after one, and the focus
  // leaving another element on the way has just hidden whatever was showing.
  document.addEventListener("click", function (e) {
    var n = find(e.target);
    if (!n || (n.agtraceTipKeys && !byKeys(n))) return;
    if (n === over || typeof n.agtraceTip === "function") show(n);
  });
  // A box left where its element used to be is worse than none.
  window.addEventListener("scroll", function () { if (current) place(current); }, { passive: true });

  window.agtraceTip = {
    // attach gives node a tip, returning node.
    //
    // A caveat — a figure that is a floor, a list nobody can explain — wears
    // the help cursor and a dotted underline where the page's CSS says so.
    // Detail that only restates what is visible at more length is
    // opts.quiet and looks like nothing until pointed at. opts.focus false
    // leaves the node out of the tab order, for the cells of a row whose
    // first cell already carries the tip. opts.hover false shows it only on
    // keyboard focus, for a node whose parts carry their own tips under a
    // pointer. text can be a function.
    attach: function (node, text, opts) {
      opts = opts || {};
      if (opts.hover === false) node.agtraceTipKeys = true;
      if (typeof text === "function") {
        node.agtraceTip = text;
        node.setAttribute("data-tip", "");
      } else {
        node.setAttribute("data-tip", text);
      }
      if (!opts.quiet) node.classList.add("has-tip");
      if (opts.focus !== false) {
        if (!node.hasAttribute("tabindex") && node.tabIndex < 0) node.setAttribute("tabindex", "0");
        node.setAttribute("aria-describedby", ID);
      }
      return node;
    }
  };
})();
