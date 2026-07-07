// Admin section progressive enhancement (OBL-15). Operator-only pages.
//
// Two responsibilities, both dependency-free and scoped to the Admin section:
//
//   1. A SEARCHABLE PROJECT SELECTOR — enhances every [data-proj-select] into a
//      type-to-filter combobox over GET /admin/projects. The submitted value lives
//      in a hidden input and is set ONLY when the operator PICKS an option (click or
//      Enter on a highlighted row). Typing free text never yields a value, so a
//      project name can never be free-typed — it must come from the known list.
//
//   2. ADMIN FORM SUBMISSION — [data-admin-form] forms build their request URL from
//      {placeholder} path params (filled by named controls) and send a JSON or form
//      body, then reload on success. This is needed because several OBL-14 endpoints
//      take path params (…/projects/{project}, …/members/{account_id}) and/or decode
//      a JSON body, neither of which a plain HTMX form post expresses.
//
// Everything here re-checks nothing on its own: every request hits an operator-gated
// endpoint that re-verifies the operator server-side.
(function () {
  "use strict";

  // ─── Searchable project selector ───────────────────────────────────────────

  var projectsCache = null;
  var projectsPromise = null;

  function loadProjects(endpoint) {
    if (projectsCache) return Promise.resolve(projectsCache);
    if (projectsPromise) return projectsPromise;
    projectsPromise = fetch(endpoint, {
      headers: { Accept: "application/json" },
      credentials: "same-origin"
    })
      .then(function (r) { return r.ok ? r.json() : []; })
      .then(function (list) { projectsCache = Array.isArray(list) ? list : []; return projectsCache; })
      .catch(function () { projectsCache = []; return projectsCache; });
    return projectsPromise;
  }

  function projectLabel(p) {
    return (p.display_name && p.display_name.trim()) ? p.display_name : p.project;
  }

  function enhanceSelector(root) {
    if (root.__enhanced) return;
    root.__enhanced = true;
    var endpoint = root.getAttribute("data-endpoint") || "/admin/projects";
    var input = root.querySelector("[data-proj-input]");
    var hidden = root.querySelector("[data-proj-value]");
    var list = root.querySelector("[data-proj-list]");
    if (!input || !hidden || !list) return;
    var kindFilter = (root.getAttribute("data-kind") || "").toLowerCase();
    var active = -1;
    var options = [];

    function close() {
      list.hidden = true;
      active = -1;
      input.setAttribute("aria-expanded", "false");
    }

    function clearValue() {
      hidden.value = "";
      root.removeAttribute("data-picked");
    }

    function render(items) {
      options = items;
      list.innerHTML = "";
      if (!items.length) {
        var empty = document.createElement("li");
        empty.className = "proj-opt proj-opt-empty";
        empty.textContent = "No matching projects";
        list.appendChild(empty);
      } else {
        items.forEach(function (p, i) {
          var li = document.createElement("li");
          li.className = "proj-opt";
          li.setAttribute("role", "option");
          li.dataset.project = p.project;
          var name = document.createElement("span");
          name.className = "proj-opt-name";
          name.textContent = projectLabel(p);
          li.appendChild(name);
          if (p.kind) {
            var chip = document.createElement("span");
            chip.className = "proj-chip proj-chip-" + p.kind;
            chip.textContent = p.kind;
            li.appendChild(chip);
          }
          if (projectLabel(p) !== p.project) {
            var raw = document.createElement("span");
            raw.className = "proj-opt-id";
            raw.textContent = p.project;
            li.appendChild(raw);
          }
          li.addEventListener("mousedown", function (e) { e.preventDefault(); pick(i); });
          list.appendChild(li);
        });
      }
      list.hidden = false;
      input.setAttribute("aria-expanded", "true");
    }

    function highlight(idx) {
      var lis = list.querySelectorAll(".proj-opt:not(.proj-opt-empty)");
      lis.forEach(function (li, i) { li.classList.toggle("active", i === idx); });
      active = idx;
      if (idx >= 0 && lis[idx]) lis[idx].scrollIntoView({ block: "nearest" });
    }

    function pick(idx) {
      var p = options[idx];
      if (!p) return;
      hidden.value = p.project;
      root.setAttribute("data-picked", "1");
      root.classList.remove("proj-invalid");
      input.value = projectLabel(p);
      close();
      root.dispatchEvent(new CustomEvent("proj:picked", { bubbles: true, detail: p }));
    }

    function filter() {
      var q = input.value.trim().toLowerCase();
      loadProjects(endpoint).then(function (all) {
        var items = all.filter(function (p) {
          if (kindFilter && (p.kind || "") !== kindFilter) return false;
          if (!q) return true;
          return p.project.toLowerCase().indexOf(q) !== -1 ||
            (p.display_name || "").toLowerCase().indexOf(q) !== -1;
        });
        render(items);
      });
    }

    input.addEventListener("focus", function () { filter(); });
    input.addEventListener("input", function () { clearValue(); filter(); });
    input.addEventListener("keydown", function (e) {
      if (list.hidden && (e.key === "ArrowDown" || e.key === "ArrowUp")) { filter(); return; }
      if (e.key === "ArrowDown") { e.preventDefault(); highlight(Math.min(active + 1, options.length - 1)); }
      else if (e.key === "ArrowUp") { e.preventDefault(); highlight(Math.max(active - 1, 0)); }
      else if (e.key === "Enter") { if (active >= 0) { e.preventDefault(); pick(active); } }
      else if (e.key === "Escape") { close(); }
    });
    // Delay close so a mousedown pick on an option registers first.
    input.addEventListener("blur", function () { setTimeout(close, 150); });
  }

  // ─── Admin form submission ─────────────────────────────────────────────────

  function controlValue(el) {
    if (el.type === "checkbox") return el.checked;
    return el.value;
  }

  function showFormError(form, msg) {
    var box = form.querySelector("[data-form-error]");
    if (box) { box.textContent = msg; box.hidden = false; }
  }

  function clearFormError(form) {
    var box = form.querySelector("[data-form-error]");
    if (box) { box.textContent = ""; box.hidden = true; }
  }

  function submitAdminForm(form) {
    clearFormError(form);

    // Every required selector must carry a picked (list-sourced) value.
    var missing = false;
    form.querySelectorAll("[data-proj-select][data-required]").forEach(function (root) {
      var h = root.querySelector("[data-proj-value]");
      if (!h || !h.value.trim()) { missing = true; root.classList.add("proj-invalid"); }
      else root.classList.remove("proj-invalid");
    });
    if (missing) { showFormError(form, "Pick a project from the list."); return; }

    var tmpl = form.getAttribute("data-url-template") || "";
    var method = (form.getAttribute("data-method") || "PUT").toUpperCase();
    var bodyMode = form.getAttribute("data-body") || "none";
    var permsField = form.getAttribute("data-perms-into");
    var consumed = {};

    var url = tmpl.replace(/\{([^}]+)\}/g, function (_, key) {
      var el = form.querySelector("[name='" + key + "']");
      consumed[key] = true;
      return encodeURIComponent(el ? String(controlValue(el)) : "");
    });

    var opts = { method: method, headers: { "HX-Request": "true" }, credentials: "same-origin" };
    var controls = form.querySelectorAll("[name]");

    if (bodyMode === "json") {
      var obj = {};
      if (permsField) {
        var bits = 0;
        form.querySelectorAll("[data-perm-bit]").forEach(function (cb) {
          if (cb.checked) bits |= (parseInt(cb.getAttribute("data-perm-bit"), 10) || 0);
        });
        obj[permsField] = bits;
      }
      controls.forEach(function (el) {
        if (consumed[el.name]) return;
        if (el.hasAttribute("data-perm-bit")) return;
        if (permsField && el.name === permsField) return;
        obj[el.name] = controlValue(el);
      });
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(obj);
    } else if (bodyMode === "form") {
      var params = new URLSearchParams();
      controls.forEach(function (el) {
        if (consumed[el.name]) return;
        if (el.type === "checkbox") { if (el.checked) params.append(el.name, "on"); return; }
        params.append(el.name, el.value);
      });
      opts.headers["Content-Type"] = "application/x-www-form-urlencoded";
      opts.body = params.toString();
    }

    fetch(url, opts).then(function (r) {
      if (r.ok) { window.location.reload(); return; }
      // A page can supply a friendlier message for a specific status (e.g. a 409
      // when deleting a profile still assigned to team members).
      var custom = form.getAttribute("data-error-" + r.status);
      if (custom) { showFormError(form, custom); return; }
      r.json().catch(function () { return {}; }).then(function (j) {
        showFormError(form, (j && j.error) ? j.error : ("Request failed (" + r.status + ")"));
      });
    }).catch(function () { showFormError(form, "Network error — try again."); });
  }

  function initAdminForms(scope) {
    (scope || document).querySelectorAll("[data-admin-form]").forEach(function (form) {
      if (form.__wired) return;
      form.__wired = true;
      form.addEventListener("submit", function (e) { e.preventDefault(); submitAdminForm(form); });
    });
  }

  function init(scope) {
    (scope || document).querySelectorAll("[data-proj-select]").forEach(enhanceSelector);
    initAdminForms(scope);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", function () { init(document); });
  } else {
    init(document);
  }
})();
