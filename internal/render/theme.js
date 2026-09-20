/* The theme is the reader's, not the page's: it is stored per browser, applied
   before the first paint so there is no flash of the wrong one, and picked up
   by every other agtrace page and tab. Loaded in <head>, ahead of the body. */
(function () {
  "use strict";
  var KEY = "agtrace-theme"; // "light" | "dark" | absent, meaning follow the OS
  var listeners = [];

  // Storage can throw or come back empty — a private window, blocked site
  // data, a preview — and the page has to render correctly when it does.
  function stored() {
    try {
      return localStorage.getItem(KEY) || "";
    } catch (e) {
      return "";
    }
  }
  function apply(v) {
    var root = document.documentElement;
    if (v) root.setAttribute("data-theme", v);
    else root.removeAttribute("data-theme");
  }
  apply(stored());

  function set(v) {
    try {
      if (v) localStorage.setItem(KEY, v);
      else localStorage.removeItem(KEY);
    } catch (e) {}
    apply(v);
    listeners.forEach(function (f) {
      try {
        f(v);
      } catch (e) {}
    });
  }

  window.agtraceTheme = {
    get: stored,
    set: set,
    // system → light → dark → system. A two-state toggle cannot get back to
    // the OS setting, and the OS setting is a real choice.
    cycle: function () {
      set({ "": "light", light: "dark", dark: "" }[stored()]);
      return stored();
    },
    label: function () {
      return "theme: " + (stored() || "system");
    },
    onChange: function (f) {
      listeners.push(f);
    },
    // wire turns a button into the toggle, keeping its label in step.
    wire: function (btn) {
      var t = window.agtraceTheme;
      btn.textContent = t.label();
      btn.setAttribute("title", "Switch between system, light and dark. Remembered in this browser.");
      btn.addEventListener("click", function () {
        t.cycle();
      });
      t.onChange(function () {
        btn.textContent = t.label();
      });
    }
  };

  // Another tab changing it changes it here too.
  addEventListener("storage", function (e) {
    if (e.key === KEY) apply(stored());
  });
})();
