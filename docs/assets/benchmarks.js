async function loadBench() {
  const meta = document.getElementById("bench-meta");
  const tbody = document.querySelector("#bench-table tbody");

  function setPlaceholder(msg) {
    tbody.innerHTML = `<tr><td colspan="6">${msg}</td></tr>`;
    if (meta) meta.textContent = "";
  }

  try {
    const resp = await fetch("./data/benchmarks.latest.json", { cache: "no-store" });
    if (!resp.ok) {
      setPlaceholder("Noch keine Daten gefunden (docs/data/benchmarks.latest.json fehlt).");
      return;
    }
    const data = await resp.json();

    if (!data || !Array.isArray(data.steps)) {
      setPlaceholder("Datenformat unbekannt.");
      return;
    }

    const startedAt = data.started_at ? new Date(data.started_at) : null;
    const profile = data.profile || "?";

    if (meta) {
      meta.textContent = `Quelle: docs/data/benchmarks.latest.json · Profil: ${profile}` +
        (startedAt ? ` · Zeitpunkt: ${startedAt.toISOString()}` : "");
    }

    const rows = data.steps.map(s => {
      const preAvail = s.pre_mem_kb && s.pre_mem_kb.MemAvailable ? (s.pre_mem_kb.MemAvailable / 1024.0) : 0;
      const postAvail = s.post_mem_kb && s.post_mem_kb.MemAvailable ? (s.post_mem_kb.MemAvailable / 1024.0) : 0;

      return `
        <tr>
          <td>${s.n ?? ""}</td>
          <td>${s.alive ?? ""}</td>
          <td>${(s.estimated_saved_mib ?? 0).toFixed(1)}</td>
          <td>${s.ksmd_ticks_delta ?? 0}</td>
          <td>${preAvail.toFixed(1)}</td>
          <td>${postAvail.toFixed(1)}</td>
        </tr>
      `;
    }).join("");

    tbody.innerHTML = rows || `<tr><td colspan="6">Keine Steps vorhanden.</td></tr>`;
  } catch (e) {
    setPlaceholder("Fehler beim Laden/Parsen der Benchmark-Daten.");
    console.error(e);
  }
}

document.addEventListener("DOMContentLoaded", loadBench);
