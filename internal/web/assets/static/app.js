// Small progressive enhancements; the UI works without them.
(function () {
  "use strict";

  // Free-text filter for the ingress and audit tables.
  function wireTableFilter(root) {
    var input = root.querySelector("[data-filter-input]");
    var rows = root.querySelectorAll("[data-filter-row]");
    if (!input || rows.length === 0) {
      return;
    }
    input.addEventListener("input", function () {
      var needle = input.value.trim().toLowerCase();
      var visible = 0;
      rows.forEach(function (row) {
        var haystack = (row.getAttribute("data-filter-row") || "").toLowerCase();
        var match = needle === "" || haystack.indexOf(needle) !== -1;
        row.hidden = !match;
        if (match) {
          visible++;
        }
      });
      var counter = root.querySelector("[data-filter-count]");
      if (counter) {
        counter.textContent = String(visible);
      }
    });
  }

  // Render timestamps in the visitor's locale; the server emits RFC 3339.
  function localizeTimes(root) {
    root.querySelectorAll("time[datetime]").forEach(function (el) {
      var parsed = new Date(el.getAttribute("datetime"));
      if (isNaN(parsed.getTime())) {
        return;
      }
      el.textContent = parsed.toLocaleString();
    });
  }

  function init(root) {
    root.querySelectorAll("[data-filter-scope]").forEach(wireTableFilter);
    localizeTimes(root);
  }

  document.addEventListener("DOMContentLoaded", function () {
    init(document);
  });

  // htmx swaps replace whole fragments, so re-apply to the new content.
  document.body.addEventListener("htmx:afterSwap", function (event) {
    init(event.target);
  });
})();
