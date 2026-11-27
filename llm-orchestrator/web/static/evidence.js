'use strict';

// Renderers of a story's evidence: the traces of the specified tests, the
// functional acceptance and the specialties' reports. They are pure: they
// take what the run recorded and return HTML, escaping every value.
window.FazTudoEvidence = ({ esc }) => {
  // What each trace status proves; only "comprovado" is a pass.
  const TRACE_TONE = {
    'comprovado': 'success', 'falhou': 'danger', 'ignorado': 'warn', 'sem teste': 'warn',
    'sem relatório': 'warn', 'não executado': 'warn', 'ID de antes da US': 'warn', 'só código de saída': '',
  };
  const traceStatus = (t) => t.status || (t.files?.length ? 'só código de saída' : 'sem teste');

  function renderTrace(trace) {
    if (!trace?.length) return '';
    const proven = trace.filter((t) => traceStatus(t) === 'comprovado').length;
    const found = trace.filter((t) => t.files?.length).length;
    const missing = trace.filter((t) => !t.files?.length);
    const reported = trace.some((t) => t.status && t.status !== 'só código de saída' && t.status !== 'sem teste');
    const shown = reported ? proven : found;
    const pct = Math.round((shown / trace.length) * 100);
    const label = reported ? 'comprovados por execução' : 'encontrados nos arquivos de teste';
    const cases = (t) => (t.cases || []).map((c) => `<code title="${esc(c.command)}">${esc(c.name)} (${esc(c.status)})</code>`).join(' ');
    return `
      <div class="trace" data-complete="${shown === trace.length}">
        <div class="trace-head"><span>Rastreabilidade dos testes</span><span class="num">${shown} de ${trace.length} ${label}</span></div>
        <div class="trace-bar" role="img" aria-label="${pct}% dos casos de teste ${label}"><span data-pct="${pct}"></span></div>
        ${missing.length ? `<div class="chips">${missing.map((t) => `<span class="chip" data-missing title="${esc(t.description || '')}">${esc(t.id)}</span>`).join('')}</div>` : ''}
        ${reported ? '' : '<p class="note">Nenhum comando com relatório por caso: o resultado comprova só os comandos, não cada critério.</p>'}
        <details class="trace-all"><summary>Ver todos</summary>
          <table>${trace.map((t) => `
            <tr><td><code>${esc(t.id)}</code></td><td>${esc(t.description || '')}</td>
            <td><span class="note" data-tone="${TRACE_TONE[traceStatus(t)] ?? ''}">${esc(traceStatus(t))}</span></td>
            <td>${cases(t) || (t.files || []).map((f) => `<code>${esc(f)}</code>`).join(' ')}</td></tr>`).join('')}
          </table>
          ${trace[0]?.commit ? `<p class="note">Commit ${esc(trace[0].commit.slice(0, 10))}.</p>` : ''}
        </details>
      </div>`;
  }

  // The functional QA's scenarios, as held to the executed evidence.
  const SCENARIO_TONE = { 'aceito': 'success', 'recusado': 'danger', 'sem evidência': 'warn', 'invalidado': 'warn' };

  function renderAcceptance(list) {
    if (!list?.length) return '';
    const accepted = list.filter((a) => a.status === 'aceito').length;
    return `
      <details class="trace-all">
        <summary>Aceitação funcional: ${accepted} de ${list.length} cenário(s) aceito(s)</summary>
        <table>${list.map((a) => `
          <tr>
            <td>${a.id ? `<code>${esc(a.id)}</code> ` : ''}${esc(a.criterion)}${a.negative ? ' <span class="note">(negativo)</span>' : ''}<br><span class="note">${esc(a.scenario || '')}</span></td>
            <td><span class="note" data-tone="${SCENARIO_TONE[a.status] || ''}">${esc(a.status)}</span></td>
            <td>${(a.tests || []).map((t) => `<code>${esc(t)}</code>`).join(' ')} ${esc(a.evidence || '')}
              ${a.note ? `<br><span class="note">${esc(a.note)}</span>` : ''}
              ${a.responsible ? `<br><span class="note">Responsável: ${esc(a.responsible)}</span>` : ''}
              ${(a.artifacts || []).length ? `<br>${a.artifacts.map((f) => `<code>${esc(f)}</code>`).join(' ')}` : ''}</td>
          </tr>`).join('')}
        </table>
      </details>`;
  }

  // What the specialties required, and what the performance specialist measured.
  function renderSpecialties(st) {
    const reports = Object.entries(st.specialties || {});
    const measures = st.measurements || [];
    if (!reports.length && !measures.length && !st.performance?.length) return '';
    const items = reports.flatMap(([id, r]) => [
      ...(r.requirements || []).map((q) => `<tr><td><code>${esc(q.id)}</code> <span class="note">${esc(id)}</span></td><td>${esc(q.text)}</td><td>${esc(q.verification)}</td></tr>`),
      ...(r.problems || []).map((p) => `<tr><td><span class="note">${esc(id)}</span></td><td colspan="2"><span class="note" data-tone="warn">${esc(p)}</span></td></tr>`),
      ...(!r.requirements?.length && r.none_reason ? [`<tr><td><span class="note">${esc(id)}</span></td><td colspan="2">${esc(r.none_reason)}</td></tr>`] : []),
    ]);
    const benches = (st.performance || []).flatMap((r) => [
      `<tr><td><strong>${esc(r.name)}</strong> <span class="note">${esc(r.workload)}</span></td><td>${esc(r.env)}, ${r.repeat} medição(ões)</td><td><span class="note" data-tone="${r.verdict === 'aprovado' ? 'success' : r.verdict === 'regressão' ? 'danger' : 'warn'}">${esc(r.verdict)}</span> ${esc(r.detail || '')}</td></tr>`,
      ...(r.metrics || []).map((m) => `<tr><td>${esc(m.name)}</td><td>${m.baseline.toFixed(2)} → ${m.candidate.toFixed(2)}</td><td>${m.change_pct >= 0 ? '+' : ''}${m.change_pct.toFixed(1)}% de piora, ruído ${m.noise_pct.toFixed(0)}%</td></tr>`),
    ]);
    const rows = measures.map((m) => `<tr><td>${esc(m.metric)}</td><td>${esc(m.value)}${m.slo ? ` (meta ${esc(m.slo)})` : ''}</td><td><span class="note" data-tone="${m.met ? 'success' : 'danger'}">${m.met ? 'atingida' : 'não atingida'}</span></td></tr>`);
    return `
      <details class="trace-all">
        <summary>Especialidades: ${reports.length} relatório(s)${measures.length ? `, ${measures.length} medição(ões)` : ''}</summary>
        <table>${items.join('')}${rows.join('')}${benches.join('')}</table>
      </details>`;
  }

  return { renderTrace, renderAcceptance, renderSpecialties };
};
