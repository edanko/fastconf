// fastconf console: progressive enhancement only; htmx owns every request.
(() => {
  const lower = (value) => (value || "").toLowerCase();

  function filterTenants() {
    const input = document.getElementById("tenant-filter");
    const query = lower(input && input.value);
    document.querySelectorAll("#tenant-list .fc-tenant").forEach((item) => {
      const code = lower(item.dataset.code);
      item.hidden = Boolean(query) && code !== "" && !code.includes(query);
    });
  }

  function filterRows() {
    const input = document.getElementById("key-filter");
    const table = document.getElementById("config-table");
    if (!input || !table) return;
    const query = lower(input.value).trim();
    let visible = 0;
    table.querySelectorAll("tbody tr").forEach((row) => {
      const matches = !query || lower(row.dataset.path).includes(query) || lower(row.dataset.value).includes(query);
      row.hidden = !matches;
      if (matches) visible += 1;
    });
    const counter = document.getElementById("row-count");
    if (counter) {
      const total = table.querySelectorAll("tbody tr").length;
      counter.textContent = query
        ? `Showing ${visible} of ${total} loaded keys`
        : `Showing ${total} of ${total} keys`;
    }
  }

  function validateJSON() {
    const textarea = document.querySelector('[data-override-form] textarea[name="value"]');
    const status = document.getElementById("json-status");
    const submit = document.querySelector('[data-override-form] button[type="submit"]');
    if (!textarea || !status || !submit) return;
    if (!textarea.value.trim()) {
      status.textContent = "Enter a JSON value.";
      status.classList.remove("is-error");
      submit.disabled = true;
      textarea.removeAttribute("aria-invalid");
      return;
    }
    try {
      JSON.parse(textarea.value);
      status.textContent = "Valid JSON · applied above every source.";
      status.classList.remove("is-error");
      submit.disabled = false;
      textarea.removeAttribute("aria-invalid");
    } catch (error) {
      status.textContent = "Invalid JSON: " + error.message;
      status.classList.add("is-error");
      submit.disabled = true;
      textarea.setAttribute("aria-invalid", "true");
    }
  }

  document.addEventListener("input", (event) => {
    if (event.target.id === "tenant-filter") filterTenants();
    if (event.target.id === "key-filter") filterRows();
    if (event.target.matches && event.target.matches('[data-override-form] textarea[name="value"]')) validateJSON();
  });

  document.addEventListener("keydown", (event) => {
    if (event.key !== "/" || event.metaKey || event.ctrlKey || event.altKey) return;
    const tag = (event.target.tagName || "").toLowerCase();
    if (tag === "input" || tag === "textarea" || event.target.isContentEditable) return;
    const search = document.getElementById("key-filter");
    if (search) {
      event.preventDefault();
      search.focus();
    }
  });

  document.addEventListener("htmx:afterSwap", (event) => {
    if (!event.detail || !event.detail.target) return;
    if (event.detail.target.id === "workspace" || event.detail.target.id === "tenant-list") {
      filterTenants();
      validateJSON();
      if (event.detail.target.id === "workspace") {
        const title = document.querySelector("#scope-title");
        if (title) document.title = "fastconf console · " + title.textContent.trim();
      }
    }
  });

  document.addEventListener("htmx:beforeRequest", (event) => {
    if (event.detail && event.detail.elt && event.detail.elt.classList) {
      event.detail.elt.setAttribute("aria-busy", "true");
    }
  });

  document.addEventListener("htmx:afterRequest", (event) => {
    if (event.detail && event.detail.elt && event.detail.elt.classList) {
      event.detail.elt.removeAttribute("aria-busy");
    }
  });

  document.addEventListener("DOMContentLoaded", () => {
    validateJSON();
    filterTenants();
  });
})();
