document.addEventListener("htmx:afterSwap", ({ detail }) => {
  if (detail.target?.id === "schedule" && document.activeElement === document.body) {
    document.getElementById("schedule-heading")?.focus();
  }
});
