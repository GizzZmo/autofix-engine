/**
 * AutoFix Client Runtime
 * Safety-net for environments without the Edge Worker.
 * - Shows tooltips on healed links
 * - Optional click interceptor that can warn on known-dead links
 */
(function () {
  const CONFIG = {
    api: null, // e.g. 'https://autofix-api.yourdomain.com/v1'
    showTooltip: true,
    interceptClicks: false,
  };

  function markHealed(link) {
    if (CONFIG.showTooltip) {
      link.title =
        "✨ This broken link was automatically repaired by AutoFix." +
        (link.dataset.autofixOriginal
          ? "\nOriginal: " + link.dataset.autofixOriginal
          : "");
    }
    link.classList.add("autofix-healed");
  }

  // Tooltip / indicator for links already rewritten at the edge
  document.addEventListener(
    "mouseover",
    function (e) {
      const link = e.target.closest("a");
      if (!link || link.dataset.autofixChecked) return;
      link.dataset.autofixChecked = "true";

      if (link.classList.contains("autofix-healed") || link.dataset.autofixOriginal) {
        markHealed(link);
        console.info("AutoFix: link was repaired at the Edge.", link.href);
      }
    },
    { passive: true }
  );

  // Optional click interceptor (disabled by default)
  if (CONFIG.interceptClicks) {
    document.addEventListener("click", function (e) {
      const link = e.target.closest("a");
      if (!link || !link.classList.contains("autofix-healed")) return;
      // Could fetch CONFIG.api + '/status?url=' + encodeURIComponent(...) here
    });
  }

  // Expose a tiny API for host pages
  window.AutoFix = {
    version: "1.1.0",
    config: CONFIG,
    markHealed: markHealed,
  };

  console.log("🛠️ AutoFix Runtime Active (v" + window.AutoFix.version + ")");
})();
