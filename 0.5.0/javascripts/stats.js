// Live run statistics (#97): fills the home metric strip
// (overrides/home.html) and the Stats page (docs/stats.md) from stats.json,
// which .github/workflows/docs.yaml publishes at the root of the site, above
// every version (/stats.json next to /main/ and /latest/). Same origin, no
// third-party call. Without a valid, fresh file both keep their neutral
// text. Names in the file come from unsigned run records: they are written
// with textContent, never as HTML.
(function () {
  var SRC = new URL("../../stats.json", document.currentScript.src).href;
  var VERSION = "aval.trianalab.io/stats-report/v1";
  var STALE_MS = 6 * 3600e3; // docs.yaml refreshes it twice an hour
  var loading;

  // load resolves to the report, or null when it is missing, malformed or
  // stale.
  function load() {
    loading =
      loading ||
      fetch(SRC, { cache: "no-cache" })
        .then(function (r) {
          return r.ok ? r.json() : null;
        })
        .catch(function () {
          return null;
        });
    return loading.then(function (s) {
      var t = s && Date.parse(s.generatedAt);
      var age = Date.now() - t;
      var ok =
        s &&
        s.version === VERSION &&
        s.total &&
        typeof s.total.runs === "number" &&
        Array.isArray(s.tests) &&
        Array.isArray(s.repositories) &&
        age < STALE_MS &&
        age > -300e3;
      return ok ? s : null;
    });
  }

  function num(n) {
    return Number(n || 0).toLocaleString("en");
  }

  function pct(r) {
    return Math.round(100 * (r || 0)) + "%";
  }

  // duration gives [number, unit] for milliseconds.
  function duration(ms) {
    var h = (ms || 0) / 3600e3;
    if (h >= 10) return [num(Math.round(h)), "h"];
    if (h >= 1) return [h.toFixed(1), "h"];
    return [num(Math.round(h * 60)), "min"];
  }

  function ago(s) {
    var m = Math.max(0, Math.round((Date.now() - Date.parse(s.generatedAt)) / 60e3));
    if (m < 1) return "just now";
    if (m < 90) return m + (m === 1 ? " minute ago" : " minutes ago");
    return Math.round(m / 60) + " hours ago";
  }

  function period(s) {
    if (!s.since) return "all records";
    var d = Math.round((Date.parse(s.generatedAt) - Date.parse(s.since)) / 864e5);
    return "last " + d + " days";
  }

  function strip(el, s) {
    var t = s.total;
    var saved = duration(t.savedDurationMs);
    var set = function (k, v) {
      el.querySelector('[data-stat="' + k + '"]').textContent = v;
    };
    set("runs", num(t.runs));
    set("hit", Math.round(100 * (t.hitRate || 0)));
    set("reused", num(t.reused));
    set("saved", saved[0]);
    set("saved-unit", saved[1]);
    var on = Math.round(10 * (t.hitRate || 0));
    el.querySelectorAll('[data-stat="bar"] i').forEach(function (i, n) {
      i.classList.toggle("on", n < on);
    });
    var r = s.repositories.length;
    set("note", "Live: " + r + (r === 1 ? " repository, " : " repositories, ") + period(s) + ", updated " + ago(s) + ".");
  }

  function cell(tr, tag, text, bold) {
    var c = document.createElement(tag);
    var t = bold ? c.appendChild(document.createElement("b")) : c;
    t.textContent = text;
    tr.appendChild(c);
  }

  function row(body, name, st, bold) {
    var tr = document.createElement("tr");
    [name, num(st.runs), num(st.reused), pct(st.hitRate), num(st.verificationFailures), duration(st.savedDurationMs).join(" ")].forEach(function (v) {
      cell(tr, "td", v, bold);
    });
    body.appendChild(tr);
  }

  function table(first, groups, key, total) {
    var t = document.createElement("table");
    var head = document.createElement("thead");
    var tr = document.createElement("tr");
    [first, "Runs", "Reused", "Hit rate", "Verification failures", "Time saved"].forEach(function (h) {
      cell(tr, "th", h);
    });
    head.appendChild(tr);
    t.appendChild(head);
    var body = document.createElement("tbody");
    groups.forEach(function (g) {
      row(body, g[key], g.stats);
    });
    row(body, "Total", total, true);
    t.appendChild(body);
    // Material's scrolling wrapper, as for tables in the markdown.
    var wrap = document.createElement("div");
    wrap.className = "md-typeset__scrollwrap";
    var inner = document.createElement("div");
    inner.className = "md-typeset__table";
    inner.appendChild(t);
    wrap.appendChild(inner);
    return wrap;
  }

  function page(el, s) {
    var p = document.createElement("p");
    p.textContent = "Updated " + ago(s) + ", " + period(s) + ", from " + s.registry + ".";
    var h1 = document.createElement("h3");
    h1.textContent = "By repository";
    var h2 = document.createElement("h3");
    h2.textContent = "By test";
    var div = document.createElement("div");
    div.append(p, h1, table("Repository", s.repositories, "repository", s.total), h2, table("Test", s.tests, "testId", s.total));
    el.replaceWith(div); // el is the neutral paragraph
  }

  function render() {
    var els = document.querySelectorAll("[data-aval-stats]");
    if (!els.length) return;
    load().then(function (s) {
      if (!s) return;
      els.forEach(function (el) {
        (el.dataset.avalStats === "page" ? page : strip)(el, s);
      });
    });
  }

  // document$ (Material) emits on the first load and after every instant
  // navigation.
  window.document$.subscribe(render);
})();
