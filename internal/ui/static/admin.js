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

  // ─── Generic searchable combobox core ──────────────────────────────────────
  //
  // Shared UI mechanics (open/close, keyboard nav, mouse pick, blur-to-close)
  // for every type-to-filter combobox in the Admin section. A per-instance
  // `cfg.source(query)` promise supplies the matching items and
  // `cfg.renderItem(item)` builds its <li> (plain content only — this core
  // adds the shared `.proj-opt`/role/mousedown wiring); it owns none of the
  // data shape or fetch/filter strategy. The project selector below (OBL-15,
  // fetch-backed) and the Access page's account selector (Command Center v2,
  // Slice 3 — its first extra consumer, filtering data already in the page)
  // both wire through this instead of duplicating the interaction logic —
  // this is the "searchable selector, everywhere" foundation item Slice 0
  // deferred.
  function wireSearchSelect(root, cfg) {
    var input = root.querySelector(cfg.inputSel);
    var list = root.querySelector(cfg.listSel);
    if (!input || !list) return null;
    var active = -1;
    var options = [];

    function close() {
      list.hidden = true;
      active = -1;
      input.setAttribute("aria-expanded", "false");
    }

    function render(items) {
      options = items;
      list.innerHTML = "";
      if (!items.length) {
        var empty = document.createElement("li");
        empty.className = "proj-opt proj-opt-empty";
        empty.textContent = cfg.emptyText || "No matches";
        list.appendChild(empty);
      } else {
        items.forEach(function (item, i) {
          var li = cfg.renderItem(item);
          li.classList.add("proj-opt");
          li.setAttribute("role", "option");
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

    function filter() {
      cfg.source(input.value.trim()).then(render);
    }

    function pick(idx) {
      var item = options[idx];
      if (!item) return;
      cfg.onPick(item);
      close();
    }

    input.addEventListener("focus", filter);
    input.addEventListener("input", function () { if (cfg.onInput) cfg.onInput(); filter(); });
    input.addEventListener("keydown", function (e) {
      if (list.hidden && (e.key === "ArrowDown" || e.key === "ArrowUp")) { filter(); return; }
      if (e.key === "ArrowDown") { e.preventDefault(); highlight(Math.min(active + 1, options.length - 1)); }
      else if (e.key === "ArrowUp") { e.preventDefault(); highlight(Math.max(active - 1, 0)); }
      else if (e.key === "Enter") { if (active >= 0) { e.preventDefault(); pick(active); } }
      else if (e.key === "Escape") { close(); }
    });
    // Delay close so a mousedown pick on an option registers first.
    input.addEventListener("blur", function () { setTimeout(close, 150); });

    return { close: close };
  }

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
    if (!input || !hidden) return;
    var kindFilter = (root.getAttribute("data-kind") || "").toLowerCase();

    function clearValue() {
      hidden.value = "";
      root.removeAttribute("data-picked");
    }

    wireSearchSelect(root, {
      inputSel: "[data-proj-input]",
      listSel: "[data-proj-list]",
      emptyText: "No matching projects",
      source: function (q) {
        return loadProjects(endpoint).then(function (all) {
          var query = q.toLowerCase();
          return all.filter(function (p) {
            if (kindFilter && (p.kind || "") !== kindFilter) return false;
            if (!query) return true;
            return p.project.toLowerCase().indexOf(query) !== -1 ||
              (p.display_name || "").toLowerCase().indexOf(query) !== -1;
          });
        });
      },
      renderItem: function (p) {
        var li = document.createElement("li");
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
        return li;
      },
      onInput: clearValue,
      onPick: function (p) {
        hidden.value = p.project;
        root.setAttribute("data-picked", "1");
        root.classList.remove("proj-invalid");
        input.value = projectLabel(p);
        root.dispatchEvent(new CustomEvent("proj:picked", { bubbles: true, detail: p }));
      }
    });
  }

  // ─── Searchable account selector (Command Center v2, Slice 3) ─────────────
  //
  // The Access page's account picker. Unlike the project selector, every
  // operator-visible account is already rendered into THIS page (see
  // accountSelector in admin_ui.templ) — so this filters that embedded list
  // client-side instead of adding a fetch. Picking an account navigates to
  // /admin/access?user={id}; it is not part of a [data-admin-form].
  function acctSourceItems(root) {
    if (root.__acctItems) return root.__acctItems;
    var items = [];
    root.querySelectorAll("[data-acct-source] li").forEach(function (li) {
      items.push({
        id: li.getAttribute("data-account-id") || "",
        username: li.getAttribute("data-username") || "",
        email: li.getAttribute("data-email") || ""
      });
    });
    root.__acctItems = items;
    return items;
  }

  function enhanceAcctSelector(root) {
    if (root.__enhanced) return;
    root.__enhanced = true;
    if (!root.querySelector("[data-acct-input]")) return;

    wireSearchSelect(root, {
      inputSel: "[data-acct-input]",
      listSel: "[data-acct-list]",
      emptyText: "No matching accounts",
      source: function (q) {
        var query = q.toLowerCase();
        var items = acctSourceItems(root).filter(function (item) {
          if (!query) return true;
          return item.username.toLowerCase().indexOf(query) !== -1 ||
            item.email.toLowerCase().indexOf(query) !== -1;
        });
        return Promise.resolve(items);
      },
      renderItem: function (item) {
        var li = document.createElement("li");
        var name = document.createElement("span");
        name.className = "proj-opt-name";
        name.textContent = item.username;
        li.appendChild(name);
        if (item.email) {
          var email = document.createElement("span");
          email.className = "acct-opt-email";
          email.textContent = item.email;
          li.appendChild(email);
        }
        return li;
      },
      onPick: function (item) {
        window.location.href = "/admin/access?user=" + encodeURIComponent(item.id);
      }
    });
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

  // setBusy toggles the same htmx-driven loading/disabled look
  // (.htmx-request .htmx-indicator, hx-disabled-elt) for controls that are
  // NOT real htmx requests (submitAdminForm's fetch() calls, and the
  // Users-v2 create/reset-password flows below) so every mutating control in
  // the Admin section shows one consistent busy state regardless of how it
  // talks to the server.
  function setBusy(el, busy) {
    if (!el) return;
    el.disabled = busy;
    el.classList.toggle("htmx-request", busy);
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

    var submitBtn = form.querySelector('button[type="submit"]');
    setBusy(submitBtn, true);

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
      setBusy(submitBtn, false);
      // A page can supply a friendlier message for a specific status (e.g. a 409
      // when deleting a profile still assigned to team members).
      var custom = form.getAttribute("data-error-" + r.status);
      if (custom) { showFormError(form, custom); return; }
      r.json().catch(function () { return {}; }).then(function (j) {
        showFormError(form, (j && j.error) ? j.error : ("Request failed (" + r.status + ")"));
      });
    }).catch(function () { setBusy(submitBtn, false); showFormError(form, "Network error — try again."); });
  }

  function initAdminForms(scope) {
    (scope || document).querySelectorAll("[data-admin-form]").forEach(function (form) {
      if (form.__wired) return;
      form.__wired = true;
      form.addEventListener("submit", function (e) { e.preventDefault(); submitAdminForm(form); });
    });
  }

  // ─── Page-level error banner (Command Center v2, Slice 2) ─────────────────
  //
  // Every /admin/* mutation used to hx-swap="none" with nowhere to surface a
  // failure — a rejected request (409 last-admin, 400 validation, etc.) just
  // silently did nothing. htmx still fires htmx:responseError/htmx:sendError
  // for a non-2xx or network-failed request even when hx-swap="none", so one
  // page-level listener can catch every admin mutation without touching each
  // hx-* control's markup.

  function showAdminError(msg) {
    var banner = document.getElementById("admin-error-banner");
    if (banner) { banner.textContent = msg; banner.hidden = false; }
  }

  function clearAdminError() {
    var banner = document.getElementById("admin-error-banner");
    if (banner) { banner.hidden = true; banner.textContent = ""; }
  }

  document.body.addEventListener("htmx:beforeRequest", function () { clearAdminError(); });
  document.body.addEventListener("htmx:responseError", function (evt) {
    var msg = "Request failed.";
    try {
      var xhr = evt.detail && evt.detail.xhr;
      if (xhr && xhr.responseText) {
        var parsed = JSON.parse(xhr.responseText);
        if (parsed && parsed.error) msg = parsed.error;
      }
    } catch (e) { /* non-JSON error body — keep the generic message */ }
    showAdminError(msg);
  });
  document.body.addEventListener("htmx:sendError", function () {
    showAdminError("Network error — try again.");
  });

  // ─── Users v2: modals, kebab menus, search, create/reset password ─────────
  //
  // Command Center v2, Slice 2. Everything here is presentation only — every
  // fetch() call below hits the SAME operator-gated endpoints the rest of the
  // Admin section uses (Slice 1), which re-check the operator server-side.

  function escapeHTML(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  function openAdminModal(id) {
    var m = document.getElementById(id);
    if (m) m.hidden = false;
  }

  function closeAdminModal(id) {
    var m = document.getElementById(id);
    if (!m) return;
    m.hidden = true;
    scrubAdminModalSecrets(id);
    var form = m.querySelector("form");
    if (form) clearFormError(form);
  }

  // scrubAdminModalSecrets purges a one-time generated password left in the
  // DOM when a password-bearing modal (create user / reset password) is
  // closed via ANY path (✕, backdrop click, Escape) — not just the flow's
  // own "Done" button (create reloads the page; reset's Done routes back
  // through closeAdminModal too, so this also covers that path). Restoring
  // the pristine, captured server-rendered template removes the password
  // node from the DOM entirely rather than merely hiding it.
  //
  // Only restores when there is actually something to scrub — either a
  // password is currently shown, or the body has already been replaced by a
  // result view with no <form> (e.g. reset-password's passwordless "Password
  // updated" state) — so an operator mid-typing in an untouched form never
  // loses their input just by clicking outside the modal.
  function scrubAdminModalSecrets(id) {
    var bodyId =
      id === "admin-create-user-modal" ? "admin-create-user-body" :
      id === "admin-reset-password-modal" ? "admin-reset-password-body" :
      null;
    if (!bodyId) return;
    var body = document.getElementById(bodyId);
    if (!body) return;
    var hasSecret = !!body.querySelector("[data-pw-value]");
    var isResultView = !body.querySelector("form");
    if (!hasSecret && !isResultView) return;
    var template = id === "admin-create-user-modal" ? createUserBodyTemplate : resetPasswordBodyTemplate;
    if (template !== null) body.innerHTML = template;
  }

  function closeAllAdminMenus(except) {
    document.querySelectorAll(".admin-menu").forEach(function (m) {
      if (m === except) return;
      m.hidden = true;
      var toggle = document.querySelector('[data-action="toggle-menu"][data-menu="' + m.id + '"]');
      if (toggle) toggle.setAttribute("aria-expanded", "false");
    });
  }

  // ── Client-side search filter over the rendered rows ──
  function filterAdminUsers(query) {
    var q = (query || "").trim().toLowerCase();
    var table = document.getElementById("admin-users-table");
    if (!table) return;
    table.querySelectorAll("tbody tr").forEach(function (row) {
      if (!q) { row.hidden = false; return; }
      var u = row.getAttribute("data-username") || "";
      var e = row.getAttribute("data-email") || "";
      row.hidden = u.indexOf(q) === -1 && e.indexOf(q) === -1;
    });
  }

  // ── Admin Projects: search box + filter chips (Admin projects redesign,
  // issue #93). Both operate over the SAME rendered #admin-projects-grid —
  // no re-fetch, no server round trip. Each top-level .projcard already
  // carries data-proj-search/data-has-children/data-paused (see
  // adminProjectCard), so filtering is a plain attribute scan, exactly like
  // filterAdminUsers above. Wired via addEventListener/data-action (never an
  // inline onclick/oninput attribute), since an inline HTML event handler
  // resolves identifiers against the GLOBAL scope, not this file's closure —
  // a bare `function foo(){}` declared in here is not reachable that way. ──
  function filterAdminProjects() {
    var grid = document.getElementById("admin-projects-grid");
    if (!grid) return;
    var searchInput = document.getElementById("admin-project-search");
    var q = ((searchInput && searchInput.value) || "").trim().toLowerCase();
    var filterBar = document.getElementById("admin-project-filters");
    var activeFilter = (filterBar && filterBar.getAttribute("data-active-filter")) || "all";
    var visible = 0;
    Array.prototype.forEach.call(grid.children, function (card) {
      if (!card.classList || !card.classList.contains("projcard")) return;
      var matchesSearch = !q || (card.getAttribute("data-proj-search") || "").indexOf(q) !== -1;
      var matchesFilter =
        activeFilter === "all" ||
        (activeFilter === "sub" && card.getAttribute("data-has-children") === "true") ||
        (activeFilter === "paused" && card.getAttribute("data-paused") === "true");
      card.hidden = !(matchesSearch && matchesFilter);
      if (!card.hidden) visible++;
    });
    // Empty state: when a search/filter combination matches no cards, surface
    // the (server-translated) "no results" element instead of a silent grid.
    var empty = document.getElementById("admin-projects-empty");
    if (empty) empty.hidden = visible !== 0;
  }

  function setAdminProjectFilter(filterBar, filter) {
    filterBar.setAttribute("data-active-filter", filter);
    filterBar.querySelectorAll(".chip").forEach(function (chip) {
      chip.classList.toggle("on", chip.getAttribute("data-filter") === filter);
    });
    filterAdminProjects();
  }

  // ── Admin project-detail page: Memorias/Acceso/Actividad tab switch
  // (Admin projects redesign, issue #93). Scoped to the closest .pdetail so
  // the SAME markup pattern could host more than one tab strip if needed. ──
  function switchAdminDetailTab(tabEl) {
    var container = tabEl.closest(".pdetail") || document;
    var paneID = tabEl.getAttribute("data-pane");
    if (!paneID) return;
    container.querySelectorAll(".tab").forEach(function (t) { t.classList.remove("on"); });
    container.querySelectorAll(".pane").forEach(function (p) { p.classList.remove("on"); });
    // The "Gestionar acceso" header button also carries data-action="switch-tab"
    // but lives OUTSIDE .pdetail (in .dactions) and isn't itself a .tab — only
    // toggle "on" for elements that actually are one.
    if (tabEl.classList.contains("tab")) tabEl.classList.add("on");
    else {
      var matchingTab = container.querySelector('.tab[data-pane="' + paneID + '"]');
      if (matchingTab) matchingTab.classList.add("on");
    }
    var pane = document.getElementById(paneID);
    if (pane) pane.classList.add("on");
  }

  // ── Password reveal box (shared markup for create + reset-password) ──
  function passwordBoxHTML(password) {
    return (
      '<div class="admin-pwbox">' +
        '<div class="admin-pwbox-top"><span class="micro-label" style="color: var(--calypso);">Generated password</span>' +
        '<button type="button" class="admin-pwbox-copy" data-action="copy-password">⧉ Copy</button></div>' +
        '<div class="admin-pwval" data-pw-value="' + escapeHTML(password) + '">' + escapeHTML(password) + "</div>" +
        '<div class="admin-pwwarn">⚠ Shown only once. Copy it now and hand it to the user — it will not be shown again.</div>' +
      "</div>"
    );
  }

  function copyAdminPassword(el) {
    var box = el.closest(".admin-pwbox");
    var valEl = box ? box.querySelector("[data-pw-value]") : null;
    var value = valEl ? valEl.getAttribute("data-pw-value") : "";
    if (!value) return;
    var original = el.textContent;
    var done = function () {
      el.textContent = "✓ Copied";
      setTimeout(function () { el.textContent = original; }, 1500);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(value).then(done).catch(done);
    } else {
      done();
    }
  }

  // ── Create user: POST /admin/users, then (role=admin) compose the
  // existing, already-audited POST /admin/users/{id}/promote — the backend
  // deliberately does NOT set is_admin at create (see apply-progress). ──
  var createUserBodyTemplate = null;

  function resetCreateUserModal() {
    var body = document.getElementById("admin-create-user-body");
    if (body && createUserBodyTemplate !== null) body.innerHTML = createUserBodyTemplate;
    var form = document.getElementById("admin-create-user-form");
    if (form) form.addEventListener("submit", function (e) { e.preventDefault(); submitCreateUser(form); });
  }

  function renderCreatedUser(data, roleLabel, warning) {
    var body = document.getElementById("admin-create-user-body");
    if (!body) return;
    var warnHTML = warning ? '<p class="admin-form-error">' + escapeHTML(warning) + "</p>" : "";
    body.innerHTML =
      '<div class="admin-field"><label>Username</label><input type="text" value="' + escapeHTML(data.username) + '" readonly></div>' +
      '<div class="admin-field"><label>Role</label> <span class="badge badge-ok">' + escapeHTML(roleLabel) + "</span></div>" +
      passwordBoxHTML(data.generated_password) +
      warnHTML +
      '<div class="admin-modal-foot"><button type="button" class="shell-button" onclick="window.location.reload()">Done</button></div>';
  }

  function submitCreateUser(form) {
    clearFormError(form);
    var username = form.querySelector('[name="username"]').value.trim();
    var email = form.querySelector('[name="email"]').value.trim();
    var roleEl = form.querySelector('[name="role"]:checked');
    var role = roleEl ? roleEl.value : "member";
    if (!username) { showFormError(form, "Username is required."); return; }
    if (!email) { showFormError(form, "A valid email is required."); return; }

    var submitBtn = form.querySelector('button[type="submit"]');
    setBusy(submitBtn, true);

    fetch("/admin/users", {
      method: "POST",
      headers: { "Content-Type": "application/json", "HX-Request": "true" },
      credentials: "same-origin",
      body: JSON.stringify({ username: username, email: email })
    }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (data) {
        return { ok: r.ok, status: r.status, data: data };
      });
    }).then(function (res) {
      if (!res.ok) {
        setBusy(submitBtn, false);
        showFormError(form, (res.data && res.data.error) || ("Request failed (" + res.status + ")"));
        return;
      }
      if (role !== "admin") { renderCreatedUser(res.data, "Member", null); return; }
      // Compose the existing promote endpoint rather than trusting the
      // backend to set is_admin at create — see apply-progress for the
      // security rationale (bypasses the audited step-up seam otherwise).
      //
      // The account already exists at this point, and its one-time password
      // is unrecoverable once lost — so THIS fetch gets its OWN catch. If it
      // fell through to the outer .catch (e.g. on a thrown network error,
      // not just a non-2xx response), the created-user view — and the
      // password with it — would never render, even though the account was
      // successfully created as a member.
      return fetch("/admin/users/" + encodeURIComponent(res.data.id) + "/promote", {
        method: "POST",
        headers: { "HX-Request": "true" },
        credentials: "same-origin"
      }).then(function (pr) {
        if (pr.ok) {
          renderCreatedUser(res.data, "Admin", null);
        } else {
          renderCreatedUser(res.data, "Member", "User created, but promoting to admin failed — promote it from the row menu instead.");
        }
      }).catch(function () {
        renderCreatedUser(res.data, "Member", "User created, but promoting to admin failed (network error) — promote it from the row menu instead.");
      });
    }).catch(function () {
      setBusy(submitBtn, false);
      showFormError(form, "Network error — try again.");
    });
  }

  // ── Edit user modal: prefilled, submitted through the generic
  // data-admin-form JSON mechanism (PUT /admin/users/{id}). ──
  function openEditUserModal(id, username, email) {
    var form = document.getElementById("admin-edit-user-form");
    if (!form) return;
    form.querySelector('[name="id"]').value = id;
    form.querySelector('[name="username"]').value = username;
    form.querySelector('[name="email"]').value = email;
    clearFormError(form);
    openAdminModal("admin-edit-user-modal");
  }

  // ── Reset password modal: POST /admin/users/{id}/password. Same one-time
  // reveal concern as create — custom fetch, not an htmx form. ──
  var resetPasswordBodyTemplate = null;

  function renderResetPasswordResult(data) {
    var body = document.getElementById("admin-reset-password-body");
    if (!body) return;
    if (data.generated_password) {
      body.innerHTML = passwordBoxHTML(data.generated_password) +
        '<div class="admin-modal-foot"><button type="button" class="shell-button" data-action="close-modal" data-modal="admin-reset-password-modal">Done</button></div>';
    } else {
      body.innerHTML =
        '<p style="color: var(--text-dim); font-size: 13px;">Password updated.</p>' +
        '<div class="admin-modal-foot"><button type="button" class="shell-button" data-action="close-modal" data-modal="admin-reset-password-modal">Done</button></div>';
    }
  }

  function submitResetPassword(form) {
    clearFormError(form);
    var id = form.querySelector('[name="id"]').value;
    var password = form.querySelector('[name="password"]').value;
    if (password && password.length < 8) {
      showFormError(form, "Password must be at least 8 characters.");
      return;
    }
    var submitBtn = form.querySelector('button[type="submit"]');
    setBusy(submitBtn, true);
    fetch("/admin/users/" + encodeURIComponent(id) + "/password", {
      method: "POST",
      headers: { "Content-Type": "application/json", "HX-Request": "true" },
      credentials: "same-origin",
      body: JSON.stringify(password ? { password: password } : {})
    }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (data) {
        return { ok: r.ok, status: r.status, data: data };
      });
    }).then(function (res) {
      setBusy(submitBtn, false);
      if (!res.ok) {
        showFormError(form, (res.data && res.data.error) || ("Request failed (" + res.status + ")"));
        return;
      }
      renderResetPasswordResult(res.data);
    }).catch(function () {
      setBusy(submitBtn, false);
      showFormError(form, "Network error — try again.");
    });
  }

  function openResetPasswordModal(id, username) {
    var body = document.getElementById("admin-reset-password-body");
    if (!body || resetPasswordBodyTemplate === null) return;
    body.innerHTML = resetPasswordBodyTemplate;
    var form = document.getElementById("admin-reset-password-form");
    if (form) {
      form.querySelector('[name="id"]').value = id;
      var target = document.getElementById("admin-reset-password-target");
      if (target) target.textContent = "Resetting the password for " + username + ".";
      form.addEventListener("submit", function (e) { e.preventDefault(); submitResetPassword(form); });
    }
    openAdminModal("admin-reset-password-modal");
  }

  // ── One delegated click handler for every data-action control: kebab
  // toggles, menu items, modal open/close, and the password copy button. ──
  function handleAdminActionClick(e) {
    var backdrop = e.target.classList && e.target.classList.contains("admin-modal-backdrop") ? e.target : null;
    if (backdrop) { closeAdminModal(backdrop.id); return; }

    var actionEl = e.target.closest ? e.target.closest("[data-action]") : null;
    if (actionEl) {
      var action = actionEl.getAttribute("data-action");
      if (action === "open-modal") {
        var modalId = actionEl.getAttribute("data-modal");
        if (modalId === "admin-create-user-modal") resetCreateUserModal();
        openAdminModal(modalId);
        closeAllAdminMenus();
      } else if (action === "close-modal") {
        closeAdminModal(actionEl.getAttribute("data-modal"));
      } else if (action === "toggle-menu") {
        var menu = document.getElementById(actionEl.getAttribute("data-menu"));
        if (menu) {
          var willOpen = menu.hidden;
          closeAllAdminMenus();
          menu.hidden = !willOpen;
          actionEl.setAttribute("aria-expanded", String(!menu.hidden));
        }
      } else if (action === "edit-user") {
        openEditUserModal(actionEl.getAttribute("data-user-id"), actionEl.getAttribute("data-username"), actionEl.getAttribute("data-email"));
        closeAllAdminMenus();
      } else if (action === "reset-password") {
        openResetPasswordModal(actionEl.getAttribute("data-user-id"), actionEl.getAttribute("data-username"));
        closeAllAdminMenus();
      } else if (action === "toggle-token-form") {
        var box = document.getElementById(actionEl.getAttribute("data-target"));
        if (box) box.hidden = !box.hidden;
      } else if (action === "copy-password") {
        copyAdminPassword(actionEl);
      } else if (action === "filter-projects") {
        var filterBar = actionEl.closest("[data-active-filter]");
        if (filterBar) setAdminProjectFilter(filterBar, actionEl.getAttribute("data-filter") || "all");
      } else if (action === "toggle-childstrip") {
        actionEl.classList.toggle("closed");
      } else if (action === "switch-tab") {
        switchAdminDetailTab(actionEl);
      }
      return;
    }

    if (!(e.target.closest && e.target.closest(".admin-menu-wrap"))) {
      closeAllAdminMenus();
    }
  }

  document.addEventListener("click", handleAdminActionClick);
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Escape") return;
    closeAllAdminMenus();
    // Route through closeAdminModal (not a bare m.hidden = true) so Escape
    // scrubs any one-time password the same way ✕/backdrop-click do.
    document.querySelectorAll(".admin-modal-backdrop").forEach(function (m) { closeAdminModal(m.id); });
  });

  // Capture the pristine server-rendered modal bodies ONCE so they can be
  // restored on next open (create/reset-password replace their body's
  // innerHTML with a result view after a successful submission).
  function captureAdminTemplates() {
    var cu = document.getElementById("admin-create-user-body");
    if (cu && createUserBodyTemplate === null) createUserBodyTemplate = cu.innerHTML;
    var rp = document.getElementById("admin-reset-password-body");
    if (rp && resetPasswordBodyTemplate === null) resetPasswordBodyTemplate = rp.innerHTML;
  }

  function init(scope) {
    (scope || document).querySelectorAll("[data-proj-select]").forEach(enhanceSelector);
    (scope || document).querySelectorAll("[data-acct-select]").forEach(enhanceAcctSelector);
    initAdminForms(scope);
    // The create/reset-password forms are (re)wired by resetCreateUserModal()
    // / openResetPasswordModal() on every open, since both replace their
    // modal body's innerHTML with the captured template first.
    captureAdminTemplates();
    // Admin Projects search box (Admin projects redesign, issue #93) — a real
    // listener, not an inline oninput, so it reaches filterAdminProjects()
    // (see that function's doc comment for why inline attributes can't).
    var projectSearch = (scope || document).querySelector ? (scope || document).querySelector("#admin-project-search") : null;
    if (projectSearch) projectSearch.addEventListener("input", filterAdminProjects);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", function () { init(document); });
  } else {
    init(document);
  }
})();
