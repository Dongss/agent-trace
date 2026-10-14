/* One-click copy for every agtrace page, shared the way the hover text is,
   so the listing and the session page cannot drift into two behaviours.

   The button is made here and nowhere else: a page draws it with button(),
   which is also why a page with its scripts off shows none, rather than a
   button that does nothing.

   It copies the whole value while sitting beside a shortened one. The short
   form is for reading; what an id gets pasted into, a resume or a search,
   wants all of it.

   It has no hover text: a box over every row's id that says "copy" was
   noise, and the icon already says it. The answer to a click is the tick.

   navigator.clipboard exists only in a secure context. localhost is one; the
   same page opened from another machine, served with --host, is not, and
   there the older execCommand path is the only one there is. */
(function () {
  "use strict";
  var SHOWN = 1500; // how long a button says it copied

  function byHand(text) {
    var prev = document.activeElement;
    var ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.setAttribute("aria-hidden", "true");
    ta.style.position = "fixed";
    ta.style.top = "-1000px";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    var ok = false;
    try { ok = document.execCommand("copy"); } catch (e) {}
    document.body.removeChild(ta);
    if (prev && prev.focus) prev.focus();
    return ok;
  }

  function write(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text).then(
        function () { return true; },
        function () { return byHand(text); });
    }
    return Promise.resolve(byHand(text));
  }

  window.agtraceCopy = {
    // button returns a button that copies value, what naming it for a
    // screen reader: "session id". The label is not shown on hover.
    button: function (value, what) {
      var b = document.createElement("button");
      b.type = "button";
      b.className = "copy";
      var timer = 0;
      function set(copied) {
        b.classList.toggle("copied", copied);
        b.setAttribute("aria-label", copied ? "Copied" : "Copy " + what);
      }
      set(false);
      // A copy that fails leaves the icon as it was: no tick, nothing claimed.
      b.addEventListener("click", function () {
        write(value).then(function (ok) {
          if (!ok) return;
          set(true);
          clearTimeout(timer);
          timer = setTimeout(function () { set(false); }, SHOWN);
        });
      });
      return b;
    }
  };
})();
