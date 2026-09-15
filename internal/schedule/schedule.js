document.addEventListener("htmx:after:swap", ({ detail }) => {
  if (detail.ctx?.target?.id === "schedule" && document.activeElement === document.body) {
    document.getElementById("schedule-heading")?.focus();
  }
});
