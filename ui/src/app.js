(function () {
  "use strict";

  // --- Queue SSE ---
  function connectQueueStream() {
    var es = new EventSource("/ui/api/queue/stream");
    es.onmessage = function (e) {
      try {
        var d = JSON.parse(e.data);
        document.getElementById("q-pending").textContent = d.pending;
        document.getElementById("q-active").textContent = d.active;
        document.getElementById("q-completed").textContent = d.completed;
        document.getElementById("q-retry").textContent = d.retry;
        document.getElementById("q-archived").textContent = d.archived;
        document.getElementById("q-failed").textContent = d.failed;
      } catch (_) {}
    };
  }
  connectQueueStream();

  // --- Vaults ---
  var vaults = [];

  function loadVaults() {
    fetch("/ui/api/vaults")
      .then(function (r) { return r.json(); })
      .then(function (data) {
        vaults = data || [];
        renderVaultTable();
        populateVaultSelect();
      })
      .catch(function () {});
  }

  function renderVaultTable() {
    var tbody = document.getElementById("vault-table-body");
    if (vaults.length === 0) {
      tbody.innerHTML = '<tr><td colspan="3" class="px-4 py-3 text-gray-500">No vaults registered</td></tr>';
      return;
    }
    tbody.innerHTML = vaults.map(function (v) {
      return '<tr class="vault-row cursor-pointer hover:bg-gray-700" data-vault="' + escapeAttr(v.vault_id) + '">' +
        '<td class="px-4 py-3 text-gray-400 w-8"><span class="expand-icon">&#9654;</span></td>' +
        '<td class="px-4 py-3 font-mono text-sm">' + escapeHtml(v.vault_id) + '</td>' +
        '<td class="px-4 py-3">' + v.device_count + '</td>' +
        '</tr>' +
        '<tr class="device-detail hidden" data-vault-detail="' + escapeAttr(v.vault_id) + '">' +
        '<td colspan="3" class="px-4 py-2 bg-gray-750"><div class="text-gray-500 text-sm px-4">Loading...</div></td>' +
        '</tr>';
    }).join("");

    tbody.querySelectorAll(".vault-row").forEach(function (row) {
      row.addEventListener("click", function () {
        toggleVaultDetail(row.getAttribute("data-vault"));
      });
    });
  }

  function toggleVaultDetail(vaultId) {
    var detailRow = document.querySelector('[data-vault-detail="' + vaultId + '"]');
    var icon = document.querySelector('[data-vault="' + vaultId + '"] .expand-icon');
    if (detailRow.classList.contains("hidden")) {
      detailRow.classList.remove("hidden");
      icon.innerHTML = "&#9660;";
      loadDevices(vaultId);
    } else {
      detailRow.classList.add("hidden");
      icon.innerHTML = "&#9654;";
    }
  }

  function loadDevices(vaultId) {
    var detailRow = document.querySelector('[data-vault-detail="' + vaultId + '"]');
    var cell = detailRow.querySelector("td");
    fetch("/ui/api/vaults/" + encodeURIComponent(vaultId) + "/devices")
      .then(function (r) { return r.json(); })
      .then(function (devices) {
        if (!devices || devices.length === 0) {
          cell.innerHTML = '<div class="text-gray-500 text-sm px-4">No devices</div>';
          return;
        }
        var html = '<table class="w-full text-sm ml-4">' +
          '<thead><tr class="text-gray-400"><th class="px-2 py-1 text-left">Party Name</th><th class="px-2 py-1 text-left">Device Type</th><th class="px-2 py-1 text-left">Registered</th></tr></thead><tbody>' +
          devices.map(function (d) {
            return '<tr class="border-t border-gray-700">' +
              '<td class="px-2 py-1 font-mono">' + escapeHtml(d.party_name) + '</td>' +
              '<td class="px-2 py-1">' + escapeHtml(d.device_type) + '</td>' +
              '<td class="px-2 py-1">' + formatDate(d.created_at) + '</td>' +
              '</tr>';
          }).join("") +
          '</tbody></table>';
        cell.innerHTML = html;
      })
      .catch(function () {
        cell.innerHTML = '<div class="text-red-400 text-sm px-4">Failed to load devices</div>';
      });
  }

  function populateVaultSelect() {
    var sel = document.getElementById("vault-select");
    sel.innerHTML = '<option value="">Select a vault...</option>' +
      vaults.map(function (v) {
        return '<option value="' + escapeAttr(v.vault_id) + '">' + escapeHtml(v.vault_id) + '</option>';
      }).join("");
  }

  loadVaults();
  setInterval(loadVaults, 10000);

  // --- Form: party mode toggle ---
  document.querySelectorAll('input[name="party-mode"]').forEach(function (radio) {
    radio.addEventListener("change", function () {
      var isCustom = this.value === "custom";
      document.getElementById("party-select").classList.toggle("hidden", isCustom);
      document.getElementById("party-custom").classList.toggle("hidden", !isCustom);
    });
  });

  // --- Form: load devices on vault select ---
  document.getElementById("vault-select").addEventListener("change", function () {
    var vaultId = this.value;
    var partySel = document.getElementById("party-select");
    partySel.innerHTML = '<option value="">Select a device...</option>';
    if (!vaultId) return;
    fetch("/ui/api/vaults/" + encodeURIComponent(vaultId) + "/devices")
      .then(function (r) { return r.json(); })
      .then(function (devices) {
        devices.forEach(function (d) {
          var opt = document.createElement("option");
          opt.value = d.party_name;
          opt.textContent = d.party_name + " (" + d.device_type + ")";
          partySel.appendChild(opt);
        });
      })
      .catch(function () {});
  });

  // --- Form: submit ---
  document.getElementById("notify-form").addEventListener("submit", function (e) {
    e.preventDefault();
    var result = document.getElementById("notify-result");
    var isCustom = document.querySelector('input[name="party-mode"]:checked').value === "custom";
    var partyId = isCustom
      ? document.getElementById("party-custom").value
      : document.getElementById("party-select").value;

    var body = {
      vault_id: document.getElementById("vault-select").value,
      vault_name: document.getElementById("vault-name").value,
      local_party_id: partyId,
      qr_code_data: document.getElementById("qr-data").value
    };

    if (!body.vault_id || !body.vault_name || !body.local_party_id || !body.qr_code_data) {
      result.textContent = "All fields are required.";
      result.className = "text-sm text-red-400";
      result.classList.remove("hidden");
      return;
    }

    fetch("/notify", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body)
    }).then(function (r) {
      if (r.ok) {
        result.textContent = "Notification sent successfully.";
        result.className = "text-sm text-green-400";
      } else {
        result.textContent = "Failed to send notification (HTTP " + r.status + ").";
        result.className = "text-sm text-red-400";
      }
      result.classList.remove("hidden");
    }).catch(function () {
      result.textContent = "Network error.";
      result.className = "text-sm text-red-400";
      result.classList.remove("hidden");
    });
  });

  // --- Helpers ---
  function escapeHtml(s) {
    var d = document.createElement("div");
    d.appendChild(document.createTextNode(s || ""));
    return d.innerHTML;
  }
  function escapeAttr(s) {
    return (s || "").replace(/&/g, "&amp;").replace(/"/g, "&quot;");
  }
  function formatDate(s) {
    if (!s) return "-";
    var d = new Date(s);
    return d.toLocaleString();
  }
})();
