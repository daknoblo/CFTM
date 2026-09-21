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

  function wireQualityChart(chart) {
    var points = Array.from(chart.querySelectorAll("[data-quality-point]"));
    var tooltip = chart.querySelector("[data-quality-tooltip]");
    var guide = chart.querySelector("[data-quality-guide]");
    var announcement = chart.closest("#tunnel-quality").querySelector("[data-quality-announcement]");
    var active = -1;

    function hide() {
      tooltip.setAttribute("hidden", "");
      guide.setAttribute("hidden", "");
    }

    function show(point, keyboard) {
      active = points.indexOf(point);
      var start = new Date(point.getAttribute("data-since"));
      var end = new Date(point.getAttribute("data-until"));
      var options = { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" };
      var time = start.toLocaleString(undefined, options) + " - " + end.toLocaleString(undefined, options);
      var response = point.getAttribute("data-response");
      var variation = point.getAttribute("data-variation");
      var counts = point.getAttribute("data-counts");
      chart.querySelector("[data-quality-time]").textContent = time;
      chart.querySelector("[data-quality-response]").textContent = response;
      chart.querySelector("[data-quality-variation]").textContent = variation;
      chart.querySelector("[data-quality-counts]").textContent = counts;
      var x = Number(point.getAttribute("data-x"));
      tooltip.setAttribute("transform", "translate(" + Math.max(50, Math.min(570, x > 500 ? x - 392 : x + 12)) + ",28)");
      guide.setAttribute("x1", String(x));
      guide.setAttribute("x2", String(x));
      tooltip.removeAttribute("hidden");
      guide.removeAttribute("hidden");
      if (keyboard) {
        announcement.textContent = time + ". " + response + ". " + variation + ". " + counts;
      }
    }

    function inspect(event) {
      var point = event.target.closest("[data-quality-point]");
      if (point) {
        show(point, false);
      } else {
        hide();
      }
    }

    chart.addEventListener("pointermove", inspect);
    chart.addEventListener("pointerdown", inspect);
    chart.addEventListener("pointerleave", hide);
    chart.addEventListener("blur", hide);
    chart.addEventListener("keydown", function (event) {
      if (event.key === "Escape") {
        hide();
      } else if (points.length && (event.key === "ArrowLeft" || event.key === "ArrowRight")) {
        event.preventDefault();
        var next = active < 0 ? (event.key === "ArrowLeft" ? points.length - 1 : 0) :
          Math.max(0, Math.min(points.length - 1, active + (event.key === "ArrowLeft" ? -1 : 1)));
        show(points[next], true);
      }
    });
  }

  function init(root) {
    root.querySelectorAll("[data-filter-scope]").forEach(wireTableFilter);
    root.querySelectorAll("[data-quality-chart]").forEach(wireQualityChart);
    localizeTimes(root);
  }

  document.addEventListener("DOMContentLoaded", function () {
    init(document);
  });

  document.addEventListener("change", function (event) {
    if (event.target.id === "quality-hostname" && !window.htmx) {
      event.target.form.requestSubmit();
    }
  });

  // htmx swaps replace whole fragments, so re-apply to the new content.
  document.body.addEventListener("htmx:afterSwap", function (event) {
    init(event.target);
  });

  ["htmx:responseError", "htmx:sendError", "htmx:timeout"].forEach(function (name) {
    document.body.addEventListener(name, function (event) {
      if (event.detail.elt && event.detail.elt.id === "tunnel-quality") {
        var message = event.detail.elt.querySelector("[data-quality-error]");
        if (message) {
          message.hidden = false;
        }
      }
    });
  });
})();
