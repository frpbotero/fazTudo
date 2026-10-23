'use strict';

(() => {
  const MAX_STORY = 200000;
  const POLL_MS = 3000;

  const TEMPLATES = {
    checkout: `# US-101: Checkout com validação antifraude em tempo real
Como cliente da plataforma, quero concluir o pagamento com verificação antifraude instantânea.

Critérios de aceitação:
- API POST /checkout em Go com validação de payload e idempotência.
- Pipeline de score de risco avaliando velocidade de transação e histórico.
- Interface responsiva com feedback imediato de aprovação ou análise.
- Cobertura de testes unitários acima de 85% e testes E2E no Playwright simulando o checkout completo.`,
    auth: `# US-204: Autenticação multi-tenant com RBAC e MFA
Como administrador da organização, quero controlar permissões por papel e exigir MFA para ações críticas.

Critérios de aceitação:
- Endpoints de autenticação JWT com rotação de chaves e revogação.
- Middlewares de autorização RBAC (Admin, Developer, Viewer).
- Telas acessíveis de configuração de MFA (TOTP com QR Code).
- Auditoria OWASP e suíte de testes de integração.`,
  };

  const STEP_STATUS = {
    pending: { label: 'Pendente', tone: 'neutral' },
    running: { label: 'Executando', tone: 'accent' },
    done: { label: 'Concluída', tone: 'success' },
    skipped: { label: 'Reutilizado', tone: 'neutral' },
    omitted: { label: 'Não necessária', tone: 'neutral' },
    failed: { label: 'Falhou', tone: 'danger' },
    warning: { label: 'Com pendências', tone: 'warn' },
    canceled: { label: 'Cancelada', tone: 'neutral' },
  };
  const RUN_RESULT = {
    succeeded: { label: 'Concluído', tone: 'success' },
    failed: { label: 'Falhou', tone: 'danger' },
    canceled: { label: 'Cancelado', tone: 'warn' },
  };
  const PROVIDER_STATUS = {
    available: { label: 'Disponível', tone: 'success' },
    cooldown: { label: 'Em cooldown', tone: 'warn' },
    unavailable: { label: 'Não instalado', tone: 'neutral' },
  };
  const LEVEL_LABEL = { info: 'info', success: 'ok', warn: 'aviso', error: 'erro' };

  const $ = (id) => document.getElementById(id);
  const el = {
    version: $('version'), conn: $('conn'), connText: $('connText'),
    themeBtn: $('themeBtn'), configBtn: $('configBtn'),
    runForm: $('runForm'), source: $('source'), story: $('story'), storyCount: $('storyCount'),
    modeNote: $('modeNote'), startBtn: $('startBtn'), startLabel: $('startLabel'), stopBtn: $('stopBtn'),
    repoForm: $('repoForm'), repoPath: $('repoPath'), repoSaveBtn: $('repoSaveBtn'),
    discovery: $('discovery'), discoveryBadge: $('discoveryBadge'),
    openMdBtn: $('openMdBtn'), mdFile: $('mdFile'), fileInfo: $('fileInfo'),
    storyDialog: $('storyDialog'), storyForm: $('storyForm'), storyDialogTitle: $('storyDialogTitle'),
    storyModeNote: $('storyModeNote'), storyList: $('storyList'), storySelCount: $('storySelCount'),
    storyBulk: $('storyBulk'), storyPreamble: $('storyPreamble'), storyError: $('storyError'),
    storyStartBtn: $('storyStartBtn'), storyStartLabel: $('storyStartLabel'), storyBar: $('storyBar'),
    providerPicker: $('providerPicker'), modelSummary: $('modelSummary'), failoverToggle: $('failoverToggle'),
    cfgDefault: $('cfgDefault'), cfgFailover: $('cfgFailover'), persistInfo: $('persistInfo'),
    providers: $('providers'), restoreAllBtn: $('restoreAllBtn'),
    runMeta: $('runMeta'), runBadge: $('runBadge'), progress: $('progress'), progressBar: $('progressBar'), steps: $('steps'),
    log: $('log'), autoScroll: $('autoScroll'), clearLogsBtn: $('clearLogsBtn'),
    configDialog: $('configDialog'), configForm: $('configForm'), configFields: $('configFields'), configError: $('configError'),
    quality: $('quality'), qualityDialog: $('qualityDialog'), qualityForm: $('qualityForm'), qualityRepo: $('qualityRepo'),
    qAuto: $('qAuto'), qSuggested: $('qSuggested'), qCustom: $('qCustom'), qChecks: $('qChecks'), qProtected: $('qProtected'),
    qMaxLine: $('qMaxLine'), qTimeout: $('qTimeout'), qCandidates: $('qCandidates'), qGit: $('qGit'), qualityError: $('qualityError'),
    diffDialog: $('diffDialog'), diffTitle: $('diffTitle'), diffMeta: $('diffMeta'), diffBody: $('diffBody'),
    toasts: $('toasts'),
  };

  let snapshot = null;
  let modeTouched = false;
  let busy = false;
  let logFilter = 'all';
  let logsClearedAt = null;
  let lastRunStart = null;
  let pollTimer = null;
  let refetchScheduled = false;
  // Open <details> panels and scrolled outputs survive the re-render that
  // every pipeline event triggers; keyed by step and position in the step.
  const openDetails = new Set();
  const scrolled = new Map();
  let lastStepsHtml = '';
  let viewStory = null; // story index pinned by the user; null follows the run
  let pendingDoc = null; // parsed document awaiting the user's choice
  let artifacts = new Map();
  let lastPersistError = '';
  let documentName = ''; // name of the loaded .md, protected during the run

  // ---------- utils ----------
  const esc = (v) => String(v ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const icon = (name) => `<svg class="icon" aria-hidden="true"><use href="#i-${name}"/></svg>`;
  const badge = ({ label, tone }) => `<span class="badge" data-tone="${tone}">${esc(label)}</span>`;
  const fmtTime = (iso) => new Date(iso).toLocaleTimeString('pt-BR', { hour12: false });
  const fmtDuration = (ms) => {
    if (ms < 1000) return `${Math.max(0, Math.round(ms))} ms`;
    const s = Math.floor(ms / 1000);
    if (s < 60) return `${(ms / 1000).toFixed(1)} s`;
    return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, '0')}s`;
  };
  const fmtCountdown = (ms) => {
    const s = Math.max(0, Math.ceil(ms / 1000));
    return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
  };

  const storage = {
    get(key) { try { return localStorage.getItem(key); } catch { return null; } },
    set(key, value) { try { localStorage.setItem(key, value); } catch { /* storage unavailable */ } },
  };

  async function api(method, path, body) {
    const opts = { method, headers: {} };
    // State-changing calls always carry JSON: the desktop webview sends a
    // body-less POST with unknown length, which the server would refuse.
    if (body === undefined && method !== 'GET') body = {};
    if (body !== undefined) {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
    const res = await fetch(path, opts);
    if (res.status === 204) return null;
    const data = await res.json().catch(() => null);
    if (!res.ok) throw new Error(data?.error || `HTTP ${res.status}`);
    return data;
  }

  function toast(message, tone = 'info') {
    const node = document.createElement('div');
    node.className = 'toast';
    node.dataset.tone = tone;
    node.setAttribute('role', tone === 'error' ? 'alert' : 'status');
    node.innerHTML = `${icon(tone === 'error' ? 'alert' : 'check')}<span>${esc(message)}</span>`;
    el.toasts.append(node);
    setTimeout(() => {
      node.dataset.leaving = '';
      setTimeout(() => node.remove(), 220);
    }, tone === 'error' ? 6000 : 3200);
  }

  // ---------- theme ----------
  function applyTheme(theme) {
    if (theme) document.documentElement.dataset.theme = theme;
    else delete document.documentElement.dataset.theme;
  }
  function currentTheme() {
    return document.documentElement.dataset.theme
      || (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
  }
  applyTheme(storage.get('theme'));
  el.themeBtn.addEventListener('click', () => {
    const next = currentTheme() === 'dark' ? 'light' : 'dark';
    applyTheme(next);
    storage.set('theme', next);
  });

  // ---------- form ----------
  function updateStoryCount() {
    const n = el.story.value.length;
    el.storyCount.textContent = n.toLocaleString('pt-BR');
    el.storyCount.parentElement.toggleAttribute('data-over', n > MAX_STORY);
  }
  function selectedMode() {
    return el.runForm.querySelector('input[name="mode"]:checked').value;
  }
  function setMode(mode) {
    el.runForm.querySelector(`input[name="mode"][value="${mode}"]`).checked = true;
    updateModeNote();
  }
  function updateModeNote() {
    const real = selectedMode() === 'real';
    el.modeNote.dataset.tone = real ? 'warn' : '';
    el.modeNote.textContent = real
      ? 'Os CLIs serão executados com permissões automáticas e consumirão tokens da sua conta.'
      : 'Fluxo completo sem chamar os CLIs e sem consumir tokens.';
  }

  el.story.value = storage.get('draft') ?? TEMPLATES.checkout;
  updateStoryCount();
  el.story.addEventListener('input', () => {
    updateStoryCount();
    storage.set('draft', el.story.value);
  });
  el.story.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      el.runForm.requestSubmit();
    }
  });
  document.querySelectorAll('[data-template]').forEach((btn) => {
    btn.addEventListener('click', () => {
      el.story.value = TEMPLATES[btn.dataset.template];
      el.fileInfo.hidden = true;
      documentName = '';
      el.story.dispatchEvent(new Event('input'));
      el.story.focus();
    });
  });
  el.runForm.querySelectorAll('input[name="mode"]').forEach((r) => r.addEventListener('change', () => {
    modeTouched = true;
    updateModeNote();
  }));

  async function startRun(stories, preamble) {
    busy = true;
    renderControls();
    try {
      await api('POST', '/api/pipeline/start', {
        stories, preamble, source: el.source.value, dry_run: selectedMode() === 'dry', document_name: documentName,
      });
      openDetails.clear();
      viewStory = null;
      return true;
    } catch (err) {
      toast(err.message, 'error');
      return false;
    } finally {
      busy = false;
      renderControls();
    }
  }

  // useModel lets a light model (Haiku) find the stories when no story
  // heading matches; only for loaded files, the server caches its answer.
  async function parseDocument(content, useModel = false) {
    return api('POST', '/api/stories/parse', { content, use_model: useModel });
  }

  el.runForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    const content = el.story.value.trim();
    if (!content) {
      toast('Informe a User Story antes de iniciar.', 'error');
      el.story.focus();
      return;
    }
    if (content.length > MAX_STORY) {
      toast(`O conteúdo excede ${MAX_STORY.toLocaleString('pt-BR')} caracteres.`, 'error');
      return;
    }
    let doc;
    try {
      doc = await parseDocument(content, Boolean(documentName));
    } catch (err) {
      toast(err.message, 'error');
      return;
    }
    if (!doc.stories?.length) {
      toast('Nenhuma User Story encontrada.', 'error');
      return;
    }
    if (doc.stories.length > 1) {
      openStoryDialog(doc);
      return;
    }
    await startRun(doc.stories, doc.preamble || '');
  });

  // ---------- markdown file ----------
  el.openMdBtn.addEventListener('click', () => el.mdFile.click());
  el.mdFile.addEventListener('change', async () => {
    const file = el.mdFile.files[0];
    el.mdFile.value = '';
    if (!file) return;
    if (file.size > MAX_STORY * 4) {
      toast('Arquivo muito grande.', 'error');
      return;
    }
    let text;
    try {
      text = await file.text();
    } catch {
      toast('Não foi possível ler o arquivo.', 'error');
      return;
    }
    el.story.value = text;
    el.story.dispatchEvent(new Event('input'));
    documentName = file.name;
    toast(`Identificando User Stories em ${file.name}…`);
    try {
      const doc = await parseDocument(text, true);
      const n = doc.stories?.length || 0;
      showFileInfo(file.name, n);
      if (doc.method === 'model') toast(`${n} User Stories identificadas pelo modelo.`, 'success');
      if (n > 1) openStoryDialog(doc);
      else if (n === 1) toast(`1 User Story carregada de ${file.name}.`, 'success');
      else toast('Nenhuma User Story encontrada no arquivo.', 'error');
    } catch (err) {
      toast(err.message, 'error');
    }
  });

  function showFileInfo(name, count) {
    el.fileInfo.hidden = false;
    el.fileInfo.innerHTML = `${icon('file')}<span>${esc(name)} · ${count} US</span>` +
      (count > 1 ? '<button type="button" class="link" data-action="choose">Escolher US</button>' : '');
  }
  el.fileInfo.addEventListener('click', async (e) => {
    if (!e.target.closest('[data-action="choose"]')) return;
    try {
      openStoryDialog(await parseDocument(el.story.value, true));
    } catch (err) {
      toast(err.message, 'error');
    }
  });
  el.story.addEventListener('input', (e) => {
    if (!e.isTrusted) return;
    el.fileInfo.hidden = true; // manual edits detach the file label
    documentName = '';
  });

  // ---------- story picker ----------
  function storyMode() {
    return el.storyForm.querySelector('input[name="storyMode"]:checked').value;
  }
  function preview(content) {
    const line = content.split('\n').slice(1).map((l) => l.trim()).find((l) => l && !l.startsWith('#'));
    return line || '';
  }

  function openStoryDialog(doc) {
    pendingDoc = doc;
    el.storyError.hidden = true;
    el.storyDialogTitle.textContent = `${doc.stories.length} User Stories encontradas`;
    el.storyForm.querySelector('input[name="storyMode"][value="sequence"]').checked = true;
    el.storyPreamble.hidden = !doc.preamble;
    el.storyPreamble.textContent = doc.preamble ? 'O contexto antes da primeira US (épico, regras gerais) será enviado junto com cada US.' : '';
    renderStoryList(true);
    el.storyDialog.showModal();
  }

  function renderStoryList(selectAll) {
    const seq = storyMode() === 'sequence';
    const prev = new Set([...el.storyList.querySelectorAll('input:checked')].map((i) => Number(i.value)));
    el.storyList.innerHTML = pendingDoc.stories.map((st, i) => {
      const checked = seq ? (selectAll || prev.has(i)) : (prev.size ? i === Math.min(...prev) : i === 0);
      return `
        <li>
          <label class="story-option">
            <input type="${seq ? 'checkbox' : 'radio'}" name="storyPick" value="${i}"${checked ? ' checked' : ''}>
            <span class="story-order">${i + 1}.</span>
            <span class="story-title" title="${esc(st.title)}">${esc(st.title)}</span>
            <span class="story-preview">${esc(preview(st.content))}</span>
          </label>
        </li>`;
    }).join('');
    el.storyBulk.hidden = !seq;
    updateStoryDialog();
  }

  function selectedStories() {
    return [...el.storyList.querySelectorAll('input:checked')].map((i) => Number(i.value)).sort((a, b) => a - b);
  }

  function updateStoryDialog() {
    const seq = storyMode() === 'sequence';
    const n = selectedStories().length;
    el.storyList.querySelectorAll('.story-option').forEach((opt) => {
      opt.classList.toggle('is-selected', opt.querySelector('input').checked);
    });
    el.storyModeNote.textContent = seq
      ? 'As US selecionadas serão desenvolvidas em sequência, na ordem do documento; cada uma parte do código entregue pelas anteriores. Uma falha interrompe a sequência.'
      : 'Somente a US escolhida será desenvolvida.';
    el.storySelCount.textContent = seq ? `${n} de ${pendingDoc.stories.length} selecionadas` : 'Escolha uma US';
    el.storyStartLabel.textContent = seq ? `Iniciar ${n} US` : 'Iniciar US';
    el.storyStartBtn.disabled = n === 0 || Boolean(snapshot?.run.running);
  }

  el.storyForm.querySelectorAll('input[name="storyMode"]').forEach((r) => r.addEventListener('change', () => renderStoryList(false)));
  el.storyList.addEventListener('change', updateStoryDialog);
  el.storyBulk.addEventListener('click', (e) => {
    const btn = e.target.closest('[data-select]');
    if (!btn) return;
    el.storyList.querySelectorAll('input').forEach((i) => { i.checked = btn.dataset.select === 'all'; });
    updateStoryDialog();
  });
  el.storyDialog.querySelectorAll('[data-close]').forEach((b) => b.addEventListener('click', () => el.storyDialog.close()));
  el.storyDialog.addEventListener('click', (e) => { if (e.target === el.storyDialog) el.storyDialog.close(); });

  el.storyForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    const picked = selectedStories().map((i) => pendingDoc.stories[i]);
    if (!picked.length) {
      el.storyError.textContent = 'Selecione ao menos uma US.';
      el.storyError.hidden = false;
      return;
    }
    if (await startRun(picked, pendingDoc.preamble || '')) {
      el.storyDialog.close();
      toast(picked.length > 1 ? `Desenvolvimento contínuo de ${picked.length} US iniciado.` : `${picked[0].title} iniciada.`, 'success');
    }
  });

  el.stopBtn.addEventListener('click', async () => {
    try {
      await api('POST', '/api/pipeline/stop');
      toast('Cancelamento solicitado.');
    } catch (err) {
      toast(err.message, 'error');
    }
  });

  // ---------- repository ----------
  let repoDirty = false;
  el.repoPath.addEventListener('input', () => { repoDirty = true; });

  el.repoForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    const path = el.repoPath.value.trim();
    if (!path) {
      toast('Informe o caminho do repositório.', 'error');
      el.repoPath.focus();
      return;
    }
    el.repoSaveBtn.disabled = true;
    try {
      const state = await api('PUT', '/api/workspace', { path });
      repoDirty = false;
      el.repoPath.value = state.path;
      toast(state.discovery ? 'Repositório mantido; discovery da sessão reaproveitado.' : 'Repositório configurado.', 'success');
    } catch (err) {
      toast(err.message, 'error');
    } finally {
      el.repoSaveBtn.disabled = false;
    }
  });

  el.discovery.addEventListener('click', async (e) => {
    if (!e.target.closest('[data-action="rediscover"]')) return;
    try {
      await api('DELETE', '/api/workspace/discovery');
      toast('O discovery será refeito na próxima execução.');
    } catch (err) {
      toast(err.message, 'error');
    }
  });

  function renderWorkspace(ws) {
    if (!repoDirty && document.activeElement !== el.repoPath) el.repoPath.value = ws.path || '';

    if (!ws.path) {
      el.discoveryBadge.dataset.tone = 'neutral';
      el.discoveryBadge.textContent = 'Não configurado';
      el.discovery.innerHTML = '<p class="note">Configure o repositório para habilitar as execuções.</p>';
      return;
    }
    const d = ws.discovery;
    if (!d) {
      el.discoveryBadge.dataset.tone = 'warn';
      el.discoveryBadge.textContent = 'Discovery pendente';
      el.discovery.innerHTML = '<p class="note">O discovery das stacks será feito na primeira execução desta sessão e reutilizado nas seguintes.</p>';
      return;
    }

    el.discoveryBadge.dataset.tone = 'success';
    el.discoveryBadge.textContent = 'Discovery feito';
    const groups = new Map();
    for (const s of d.stacks || []) {
      if (!groups.has(s.dir)) groups.set(s.dir, []);
      groups.get(s.dir).push(s);
    }
    const stacks = groups.size
      ? [...groups].map(([dir, items]) => `
          <div class="stack-group">
            <span class="stack-dir" title="${esc(dir)}">${esc(dir === '.' ? '(raiz)' : `${dir}/`)}</span>
            <span class="stack-chips">${items.map((s) => `<span class="stack-chip" title="${esc(s.evidence)}">${esc(s.name)}</span>`).join('')}</span>
          </div>`).join('')
      : '<p class="note">Nenhum manifesto reconhecido; veja o relatório na etapa de discovery.</p>';
    const via = [d.provider, d.model].filter(Boolean).join(' · ');
    el.discovery.innerHTML = `
      <div class="discovery-meta">
        <span>Feito às ${esc(fmtTime(d.created_at))}${via ? ` via ${esc(via)}` : ''}${d.simulated ? ' (simulado)' : ''}</span>
        <button class="btn btn-ghost btn-sm" type="button" data-action="rediscover">${icon('rotate')}Refazer</button>
      </div>
      <div class="stack-groups">${stacks}</div>`;
  }

  // ---------- quality gates ----------
  const commandLines = (checks) => (checks || []).map((c) => `${c.name}: ${c.run}`).join('\n');

  function parseCommands(text) {
    return text.split('\n').map((l) => l.trim()).filter(Boolean).map((line) => {
      const i = line.indexOf(': ');
      return i > 0 ? { name: line.slice(0, i).trim(), run: line.slice(i + 2).trim() } : { name: line, run: line };
    });
  }

  function renderQuality(ws) {
    el.quality.hidden = !ws.path;
    if (!ws.path) return;
    const p = ws.policy || {};
    const checks = p.auto_checks ? ws.suggested || [] : p.checks || [];
    const origin = p.auto_checks ? 'detectados' : 'personalizados';
    const chips = checks.length
      ? checks.map((c) => `<span class="stack-chip" title="${esc(c.run)}">${esc(c.name)}</span>`).join('')
      : `<span class="note" data-tone="warn">Nenhum comando ${p.auto_checks ? 'detectado' : 'configurado'}: apenas checagens estáticas.</span>`;
    const protectedList = (p.protected || []).map((x) => `<code>${esc(x)}</code>`).join(' ');
    el.quality.innerHTML = `
      <div class="discovery-meta">
        <span class="quality-title">${icon('shield')}Portões de qualidade</span>
        <button class="btn btn-ghost btn-sm" type="button" data-action="quality">Editar</button>
      </div>
      <dl class="kv quality-kv">
        <dt>Comandos</dt><dd class="wrap"><span class="stack-chips">${chips}</span><span class="note">${esc(origin)}</span></dd>
        <dt>Protegidos</dt><dd class="wrap">${protectedList || '<span class="note">nenhum</span>'}</dd>
        <dt>Linhas</dt><dd>até ${esc(p.max_line_length)} caracteres</dd>
        <dt>Git</dt><dd>${p.git ? 'branch por US, commit por etapa' : 'desativado'}</dd>
        <dt>Candidatos</dt><dd>${p.candidates > 1 ? `best-of-${esc(p.candidates)}` : '1 por implementação'}</dd>
      </dl>`;
  }

  function syncQualityForm() {
    el.qCustom.hidden = el.qAuto.checked;
    el.qSuggested.hidden = !el.qAuto.checked;
  }

  function openQuality() {
    const ws = snapshot?.workspace;
    if (!ws?.path) return;
    const p = ws.policy;
    el.qualityError.hidden = true;
    el.qualityRepo.textContent = `Valem para ${ws.path}.`;
    el.qAuto.checked = p.auto_checks;
    const suggested = ws.suggested || [];
    el.qSuggested.innerHTML = suggested.length
      ? suggested.map((c) => `<li><span>${esc(c.name)}</span><code>${esc(c.run)}</code></li>`).join('')
      : '<li class="note">Nenhum comando encontrado nos manifestos (package.json, go.mod, pyproject.toml…).</li>';
    el.qChecks.value = commandLines(p.checks?.length ? p.checks : suggested);
    el.qProtected.value = (p.protected || []).join('\n');
    el.qMaxLine.value = p.max_line_length;
    el.qTimeout.value = p.check_timeout_s;
    el.qCandidates.value = String(p.candidates || 1);
    el.qGit.checked = p.git;
    syncQualityForm();
    el.qualityDialog.showModal();
  }

  el.quality.addEventListener('click', (e) => {
    if (e.target.closest('[data-action="quality"]')) openQuality();
  });
  el.qAuto.addEventListener('change', syncQualityForm);
  el.qualityDialog.querySelectorAll('[data-close]').forEach((b) => b.addEventListener('click', () => el.qualityDialog.close()));
  el.qualityDialog.addEventListener('click', (e) => { if (e.target === el.qualityDialog) el.qualityDialog.close(); });

  el.qualityForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    const policy = {
      auto_checks: el.qAuto.checked,
      checks: parseCommands(el.qChecks.value),
      protected: el.qProtected.value.split('\n').map((l) => l.trim()).filter(Boolean),
      git: el.qGit.checked,
      max_line_length: Number(el.qMaxLine.value) || 0,
      check_timeout_s: Number(el.qTimeout.value) || 0,
      candidates: Number(el.qCandidates.value) || 1,
    };
    try {
      await api('PUT', '/api/workspace/policy', policy);
      el.qualityDialog.close();
      toast('Portões de qualidade salvos.', 'success');
    } catch (err) {
      el.qualityError.textContent = err.message;
      el.qualityError.hidden = false;
    }
  });

  // ---------- diff ----------
  async function openDiff(hash, title) {
    el.diffTitle.textContent = `Commit ${hash}`;
    el.diffMeta.textContent = title || '';
    el.diffBody.textContent = 'Carregando…';
    el.diffDialog.showModal();
    try {
      const { patch } = await api('GET', `/api/git/commits/${encodeURIComponent(hash)}`);
      el.diffBody.innerHTML = patch.split('\n').map((line) => {
        let kind = '';
        if (line.startsWith('+++') || line.startsWith('---') || line.startsWith('diff --git')) kind = 'file';
        else if (line.startsWith('+')) kind = 'add';
        else if (line.startsWith('-')) kind = 'del';
        else if (line.startsWith('@@')) kind = 'hunk';
        return `<span class="diff-line"${kind ? ` data-kind="${kind}"` : ''}>${esc(line)}</span>`;
      }).join('\n');
    } catch (err) {
      el.diffBody.textContent = err.message;
    }
  }
  el.diffDialog.querySelectorAll('[data-close]').forEach((b) => b.addEventListener('click', () => el.diffDialog.close()));
  el.diffDialog.addEventListener('click', (e) => { if (e.target === el.diffDialog) el.diffDialog.close(); });

  // ---------- providers ----------
  el.providers.addEventListener('click', async (e) => {
    const btn = e.target.closest('button[data-action]');
    if (!btn) return;
    const { action, id } = btn.dataset;
    try {
      if (action === 'exhaust') {
        await api('POST', `/api/providers/${encodeURIComponent(id)}/exhaust`, { seconds: 45 });
        toast('Limite de uso simulado por 45 s.');
      } else {
        await api('POST', `/api/providers/${encodeURIComponent(id)}/restore`);
      }
    } catch (err) {
      toast(err.message, 'error');
    }
  });
  el.restoreAllBtn.addEventListener('click', async () => {
    try {
      await api('POST', '/api/providers/restore');
      toast('Cotas restauradas.', 'success');
    } catch (err) {
      toast(err.message, 'error');
    }
  });

  function renderProviders(providers) {
    el.providers.innerHTML = providers.map((p) => {
      const status = PROVIDER_STATUS[p.status] || PROVIDER_STATUS.available;
      let foot;
      if (p.status === 'cooldown') {
        foot = `
          <span class="countdown">${icon('clock')}Libera às ${esc(fmtTime(p.reset_at))} · <span data-countdown="${esc(p.reset_at)}">—</span></span>
          <button class="btn btn-sm" type="button" data-action="restore" data-id="${esc(p.id)}">Restaurar</button>`;
      } else {
        let hint = p.installed ? 'CLI encontrado no PATH' : 'CLI não encontrado · somente simulação';
        if (!p.default && snapshot?.config && !snapshot.config.failover) hint = 'Fora de uso · failover desativado';
        foot = `
          <span>${esc(hint)}</span>
          <button class="btn btn-ghost btn-sm" type="button" data-action="exhaust" data-id="${esc(p.id)}">${icon('zap')}Simular limite</button>`;
      }
      return `
        <div class="provider" data-status="${esc(p.status)}">
          <div class="provider-head">
            <div class="provider-name"><span>${esc(p.name)}</span><code>${esc(p.binary)}</code>${p.default ? '<span class="chip-default">Padrão</span>' : ''}</div>
            ${badge(status)}
          </div>
          <dl class="kv">
            <dt>Heavy</dt><dd title="${esc(p.heavy_model)}">${esc(p.heavy_model)}</dd>
            <dt>Standard</dt><dd title="${esc(p.std_model)}">${esc(p.std_model)}</dd>
            <dt>Esforço</dt><dd>${esc(p.effort || 'padrão')}</dd>
          </dl>
          <div class="provider-foot">${foot}</div>
        </div>`;
    }).join('');
  }

  // ---------- pipeline ----------
  function panelKey(node, selector) {
    const step = node.closest('.step');
    if (!step) return '';
    return `${step.dataset.id}|${selector}|${[...step.querySelectorAll(selector)].indexOf(node)}`;
  }
  el.steps.addEventListener('toggle', (e) => {
    if (e.target.tagName !== 'DETAILS') return;
    const key = panelKey(e.target, 'details');
    if (!key) return;
    if (e.target.open) openDetails.add(key); else openDetails.delete(key);
  }, true);
  el.steps.addEventListener('scroll', (e) => {
    if (e.target.tagName !== 'PRE') return;
    const key = panelKey(e.target, 'pre');
    if (key) scrolled.set(key, e.target.scrollTop);
  }, true);
  el.steps.addEventListener('click', async (e) => {
    const retry = e.target.closest('button[data-retry-step]');
    if (retry) {
      retry.disabled = true;
      try {
        await api('POST', '/api/pipeline/retry', {
          story: Number(retry.dataset.retryStory),
          step: retry.dataset.retryStep,
        });
        openDetails.clear();
        viewStory = null;
        toast('Etapa reiniciada. As validações seguintes serão executadas novamente.', 'success');
      } catch (err) {
        retry.disabled = false;
        toast(err.message, 'error');
      }
      return;
    }
    const diff = e.target.closest('button[data-diff]');
    if (diff) {
      openDiff(diff.dataset.diff, diff.dataset.title);
      return;
    }
    const btn = e.target.closest('button[data-copy]');
    if (!btn) return;
    e.preventDefault();
    try {
      await navigator.clipboard.writeText(artifacts.get(btn.dataset.copy) || '');
      toast('Artefato copiado.', 'success');
    } catch {
      toast('Não foi possível acessar a área de transferência.', 'error');
    }
  });
  el.storyBar.addEventListener('click', (e) => {
    const tab = e.target.closest('[data-story]');
    if (!tab) return;
    const i = Number(tab.dataset.story);
    viewStory = i === Math.max(snapshot.run.current, 0) ? null : i;
    renderRun(snapshot.run);
  });

  function stepMarker(s, index) {
    switch (s.status) {
      case 'running': return '<span class="spinner" aria-hidden="true"></span>';
      case 'done':
      case 'skipped': return icon('check');
      case 'failed': return icon('x');
      case 'warning': return icon('alert');
      case 'canceled': return icon('ban');
      case 'omitted': return icon('minus');
      default: return s.kind === 'fix' ? icon('wrench') : String(index + 1);
    }
  }

  const PLAN_SOURCE = {
    architect: '',
    heuristic: 'Simulação: escolha por palavras-chave da US.',
    fallback: 'O arquiteto não retornou um plano; todos os agentes foram executados.',
  };

  function renderPlan(plan, steps) {
    if (!plan) return '';
    const titles = new Map(steps.map((s) => [s.id, s.title]));
    const chips = plan.agents.map((id) => `<span class="stack-chip">${esc(titles.get(id) || id)}</span>`).join('');
    const note = PLAN_SOURCE[plan.source];
    return `
      <div class="plan-box">
        <div class="plan-head"><span class="plan-label">Agentes selecionados</span><span class="stack-chips">${chips}</span></div>
        ${plan.reason ? `<p class="plan-reason">${esc(plan.reason)}</p>` : ''}
        ${note ? `<p class="note">${esc(note)}</p>` : ''}
      </div>`;
  }

  // Command output quoted in a fenced block is collapsed under the summary.
  function issueDescription(text) {
    const at = text.indexOf('```');
    if (at < 0) return `<span class="issue-desc">${esc(text)}</span>`;
    const code = text.slice(at).replace(/^```[^\n]*\n?/, '').replace(/\n?```\s*$/, '');
    return `<span class="issue-desc">${esc(text.slice(0, at).trim())}</span>
      <details class="check-out"><summary>Ver saída</summary><pre>${esc(code)}</pre></details>`;
  }

  function renderIssues(issues, title) {
    if (!issues?.length) return '';
    return `
      <details class="issues">
        <summary class="issues-title"><span>${esc(title)}</span><span>${issues.length} item${issues.length === 1 ? '' : 's'}</span></summary>
        <ul>${issues.map((is) => `
          <li>
            ${is.severity ? `<span class="sev" data-sev="${esc(is.severity)}">${esc(is.severity)}</span>` : ''}
            <span class="issue-agent">${esc(is.agent)}</span>
            ${issueDescription(is.description)}
            ${is.location ? `<code>${esc(is.location)}</code>` : ''}
          </li>`).join('')}
        </ul>
      </details>`;
  }

  function renderChecks(checks) {
    if (!checks?.length) return '';
    return `
      <ul class="checks">${checks.map((c) => `
        <li data-passed="${c.passed}">
          <span class="check-icon">${icon(c.passed ? 'check' : 'x')}</span>
          <span class="check-name">${esc(c.name)}</span>
          <code title="${esc(c.command)}">${esc(c.command)}</code>
          <span class="time">${esc(fmtDuration(c.duration_ms))}</span>
          ${!c.passed && c.output ? `<details class="check-out"><summary>Saída (${c.timed_out ? 'tempo esgotado' : `código ${esc(c.exit_code)}`})</summary><pre>${esc(c.output)}</pre></details>` : ''}
        </li>`).join('')}
      </ul>`;
  }

  function renderCandidates(list) {
    if (!list?.length) return '';
    return `
      <ul class="candidates">${list.map((c) => {
        let result = c.note || (c.passed ? 'passou nas verificações' : `${c.issues} problema${c.issues === 1 ? '' : 's'}`);
        if (c.chosen) result += ' · escolhido';
        return `
        <li data-chosen="${Boolean(c.chosen)}" data-passed="${Boolean(c.passed)}">
          <span class="cand-id">${esc(c.id)}</span>
          <span class="cand-strategy">${esc(c.strategy)}</span>
          <span class="cand-result">${esc(result)}</span>
        </li>`;
      }).join('')}
      </ul>`;
  }

  function renderCommit(s) {
    const c = s.commit;
    if (!c) return '';
    const add = c.files.reduce((n, f) => n + f.added, 0);
    const del = c.files.reduce((n, f) => n + f.deleted, 0);
    const names = c.files.map((f) => f.path).join('\n');
    return `
      <div class="commit">
        ${icon('branch')}<code>${esc(c.hash)}</code>
        <span title="${esc(names)}">${c.files.length} arquivo${c.files.length === 1 ? '' : 's'}</span>
        <span class="diffstat"><span data-kind="add">+${add}</span> <span data-kind="del">−${del}</span></span>
        <button class="link" type="button" data-diff="${esc(c.hash)}" data-title="${esc(s.title)}">Ver diff</button>
      </div>`;
  }

  function renderTrace(trace) {
    if (!trace?.length) return '';
    const found = trace.filter((t) => t.files?.length).length;
    const missing = trace.filter((t) => !t.files?.length);
    const pct = Math.round((found / trace.length) * 100);
    return `
      <div class="trace" data-complete="${missing.length === 0}">
        <div class="trace-head">
          <span class="plan-label">Rastreabilidade dos testes</span>
          <span class="trace-count">${found} de ${trace.length}</span>
        </div>
        <div class="trace-bar" role="img" aria-label="${pct}% dos casos de teste encontrados"><span data-pct="${pct}"></span></div>
        ${missing.length ? `<div class="stack-chips">${missing.map((t) => `<span class="stack-chip" data-missing title="${esc(t.description || '')}">${esc(t.id)}</span>`).join('')}</div>` : ''}
        <details class="trace-all"><summary>Ver todos</summary>
          <table>${trace.map((t) => `
            <tr><td><code>${esc(t.id)}</code></td><td>${esc(t.description || '')}</td>
            <td>${t.files?.length ? t.files.map((f) => `<code>${esc(f)}</code>`).join(' ') : '<span class="note" data-tone="warn">sem teste</span>'}</td></tr>`).join('')}
          </table>
        </details>
      </div>`;
  }

  const isVerifier = (s) => /^checks(-verify-\d+)?$/.test(s.id);

  function storyTabIcon(status) {
    switch (status) {
      case 'running': return '<span class="spinner" aria-hidden="true"></span>';
      case 'done': return icon('check');
      case 'failed': return icon('x');
      case 'skipped': return icon('alert'); // blocked in Passo 0
      case 'warning': return icon('alert'); // delivered with pending issues
      default: return '<span class="dot" aria-hidden="true"></span>';
    }
  }

  function renderStoryBar(run, shown) {
    const list = run.stories || [];
    el.storyBar.hidden = list.length < 2;
    if (list.length < 2) return;
    el.storyBar.innerHTML = list.map((st, i) => `
      <button type="button" class="story-tab" role="tab" data-story="${i}" data-status="${esc(st.status)}"
              aria-selected="${i === shown}" title="${esc(st.title)}">
        ${storyTabIcon(st.status)}<span class="tab-title">${esc(st.id || st.title)}</span>
      </button>`).join('');
  }

  function renderRun(run) {
    const list = run.stories || [];
    const current = Math.max(run.current ?? 0, 0);
    const shown = viewStory !== null && viewStory < list.length ? viewStory : current;
    const story = list[shown] || { steps: [] };
    const allSteps = [run.discovery, ...(story.steps || [])].filter(Boolean);
    const hiddenSteps = allSteps.filter((s) => s.status === 'canceled' || s.status === 'omitted');
    const steps = allSteps.filter((s) => s.status !== 'canceled' && s.status !== 'omitted');

    const finished = (s) => ['done', 'skipped', 'omitted', 'warning'].includes(s.status);
    const total = 1 + list.reduce((n, st) => n + st.steps.length, 0);
    const done = (run.discovery && finished(run.discovery) ? 1 : 0) +
      list.reduce((n, st) => n + st.steps.filter(finished).length, 0);
    el.progressBar.style.width = `${total ? (done / total) * 100 : 0}%`;
    el.progress.setAttribute('aria-valuemax', String(total));
    el.progress.setAttribute('aria-valuenow', String(done));
    el.progress.dataset.result = run.result || '';

    let state = { label: 'Ocioso', tone: 'neutral' };
    if (run.running) state = { label: 'Em execução', tone: 'accent' };
    else if (run.result) state = RUN_RESULT[run.result];
    el.runBadge.dataset.tone = state.tone;
    el.runBadge.textContent = state.label;

    if (run.started_at) {
      const parts = [];
      if (list.length > 1) {
        const delivered = list.filter((st) => st.status === 'done' || st.status === 'warning').length;
        parts.push(`US ${Math.min(current + 1, list.length)} de ${list.length}`, `${delivered} entregue${delivered === 1 ? '' : 's'}`);
      } else {
        parts.push(`${done} de ${total} etapas`);
      }
      parts.push(run.dry_run ? 'Simulação' : 'Execução real');
      if (story.branch) parts.push(story.branch);
      if (run.repository) parts.push(run.repository.split('/').filter(Boolean).pop() || run.repository);
      parts.push(`Início ${fmtTime(run.started_at)}`);
      el.runMeta.innerHTML = `${parts.map(esc).join(' · ')} · <span data-elapsed="${esc(run.started_at)}"${run.finished_at ? ` data-end="${esc(run.finished_at)}"` : ''}></span>`;
    } else {
      el.runMeta.textContent = 'Nenhuma execução iniciada.';
    }

    renderStoryBar(run, shown);

    artifacts = new Map();
    let lastVerifier = -1;
    steps.forEach((s, i) => { if (i > 0 && isVerifier(s) && s.status !== 'pending' && s.status !== 'omitted') lastVerifier = i; });
    const html = steps.map((s, i) => {
      const key = i === 0 ? 'discovery' : `${shown}:${s.id}`;
      if (s.output) artifacts.set(key, s.output);
      const st = s.status === 'skipped' && i > 0 ? { label: 'Dispensada', tone: 'neutral' } : STEP_STATUS[s.status] || STEP_STATUS.pending;
      let time = '';
      if (s.status === 'running' && s.started_at) time = `<span class="time" data-elapsed="${esc(s.started_at)}"></span>`;
      else if (s.duration_ms) time = `<span class="time">${esc(fmtDuration(s.duration_ms))}</span>`;
      const model = s.model ? `<span class="model" title="${esc(s.provider)}">${esc(s.provider)} · ${esc(s.model)}</span>` : '';
      const artifact = s.output ? `
        <details class="artifact">
          <summary>${icon('chevron')}${i === 0 ? 'Relatório de discovery' : 'Artefato'}
            <button class="btn btn-ghost btn-sm" type="button" data-copy="${esc(key)}">${icon('copy')}Copiar</button>
          </summary>
          <pre>${esc(s.output)}</pre>
        </details>` : '';
      const error = s.error ? `<div class="step-error">${esc(s.error)}</div>` : '';
      let extra = renderChecks(s.checks) + renderCandidates(s.candidates);
      if (s.id === 'arch') extra = renderPlan(story.plan, story.steps || []);
      else if (s.kind === 'fix') extra = renderIssues(s.issues, 'Correções solicitadas');
      else if (s.issues?.length) extra += renderIssues(s.issues, ['failed', 'warning'].includes(s.status) ? 'Problemas não resolvidos' : 'Problemas encontrados');
      if (i === lastVerifier) extra += renderTrace(story.trace);
      extra += renderCommit(s);
      const kindTag = s.kind === 'fix' ? '<span class="kind-tag" data-kind="fix">Correção</span>'
        : s.kind === 'verify' ? '<span class="kind-tag" data-kind="verify">Verificação</span>' : '';
      const storyNote = i === 1 && list.length > 1 && story.title ? `<li class="step-story" aria-hidden="true">${esc(story.title)}</li>` : '';
      return `
        ${storyNote}
        <li class="step" data-status="${esc(s.status)}" data-kind="${esc(s.kind || '')}" data-id="${esc(key)}">
          <div class="marker">${stepMarker(s, i)}</div>
          <div class="step-body">
            <div class="step-head">
              <div>
                <div class="step-title">${esc(s.title)}</div>
                <div class="step-meta">${kindTag}<span>${esc(s.role)}</span>${s.tier ? `<span class="tier">${esc(s.tier)}</span>` : ''}${s.note ? `<span>${esc(s.note)}</span>` : ''}</div>
              </div>
              <div class="step-side">
                ${model}${time}${s.status !== 'pending' ? badge(st) : ''}
                ${s.status === 'failed' && !run.running ? `<button class="btn btn-sm" type="button" data-retry-step="${esc(s.id)}" data-retry-story="${i === 0 ? -1 : shown}">${icon('rotate')}Refazer etapa</button>` : ''}
              </div>
            </div>
            ${error}${extra}${artifact}
          </div>
        </li>`;
    }).join('') + (hiddenSteps.length ? `
      <li class="step-summary">
        <span class="summary-line"></span>
        <span>${hiddenSteps.length} etapa${hiddenSteps.length === 1 ? '' : 's'} não executada${hiddenSteps.length === 1 ? '' : 's'}</span>
      </li>` : '');
    if (html === lastStepsHtml) return; // nothing changed: keep the DOM (and what the user opened)
    lastStepsHtml = html;
    el.steps.innerHTML = html;
    el.steps.querySelectorAll('details').forEach((d) => { if (openDetails.has(panelKey(d, 'details'))) d.open = true; });
    el.steps.querySelectorAll('pre').forEach((p) => {
      const top = scrolled.get(panelKey(p, 'pre'));
      if (top) p.scrollTop = top;
    });
    // The CSP forbids inline style attributes; widths are set through the CSSOM.
    el.steps.querySelectorAll('[data-pct]').forEach((n) => { n.style.width = `${n.dataset.pct}%`; });
  }

  // ---------- logs ----------
  document.querySelectorAll('input[name="logFilter"]').forEach((r) => r.addEventListener('change', () => {
    logFilter = r.value;
    renderLogs(snapshot?.run.logs || [], true);
  }));
  el.clearLogsBtn.addEventListener('click', () => {
    const logs = snapshot?.run.logs || [];
    logsClearedAt = logs.length ? logs[logs.length - 1].time : new Date().toISOString();
    renderLogs(logs, true);
  });

  function renderLogs(logs, force = false) {
    const visible = logs.filter((l) =>
      (!logsClearedAt || l.time > logsClearedAt) &&
      (logFilter === 'all' || l.level === 'warn' || l.level === 'error'));

    const stick = el.autoScroll.checked;
    if (!visible.length) {
      el.log.innerHTML = '<div class="log-empty">Nenhum evento para exibir.</div>';
      return;
    }
    const html = visible.map((l) => `
      <div class="log-row" data-level="${esc(l.level)}">
        <span class="log-time">${esc(fmtTime(l.time))}</span>
        <span class="log-level">${esc(LEVEL_LABEL[l.level] || l.level)}</span>
        <span class="log-msg">${esc(l.message)}</span>
      </div>`).join('');
    if (force || html !== el.log.dataset.html) {
      el.log.innerHTML = html;
      el.log.dataset.html = html;
      if (stick) el.log.scrollTop = el.log.scrollHeight;
    }
  }

  // ---------- provider & models ----------
  const CUSTOM = '__custom__';

  async function updateRouting(update, message) {
    try {
      await api('PUT', '/api/config', update);
      if (message) toast(message, 'success');
      return true;
    } catch (err) {
      toast(err.message, 'error');
      return false;
    }
  }

  function modelLabel(p, id) {
    return p.models.find((m) => m.id === id)?.label || id;
  }

  function renderProviderPicker(data) {
    const cfg = data.config;
    const providers = data.providers;
    const signature = providers.map((p) => p.id).sort().join(',');
    if (el.providerPicker.dataset.sig !== signature) {
      el.providerPicker.dataset.sig = signature;
      el.providerPicker.innerHTML = [...providers].sort((a, b) => a.id.localeCompare(b.id)).map((p) => `
        <label><input type="radio" name="defaultProvider" value="${esc(p.id)}"><span>${esc(p.name)}</span></label>`).join('');
    }
    el.providerPicker.querySelectorAll('input').forEach((i) => { i.checked = i.value === cfg.default_provider; });
    el.failoverToggle.checked = cfg.failover;

    const p = providers.find((x) => x.id === cfg.default_provider);
    if (!p) return;
    el.modelSummary.innerHTML = `
      <span class="k">Heavy</span><span class="v" title="${esc(p.heavy_model)}">${esc(modelLabel(p, p.heavy_model))}</span>
      <span class="k">Standard</span><span class="v" title="${esc(p.std_model)}">${esc(modelLabel(p, p.std_model))}</span>
      <span class="k">Esforço</span><span class="v">${esc(p.effort || 'padrão')}</span>
      <button type="button" class="link edit" data-action="models">Alterar modelos</button>`;
  }

  el.providerPicker.addEventListener('change', (e) => {
    const id = e.target.value;
    const name = snapshot?.providers.find((p) => p.id === id)?.name || id;
    updateRouting({ default_provider: id }, `${name} definido como provedor padrão.`);
  });
  el.failoverToggle.addEventListener('change', () => {
    updateRouting({ failover: el.failoverToggle.checked },
      el.failoverToggle.checked ? 'Failover ativado.' : 'Failover desativado: somente o provedor padrão será usado.');
  });
  el.modelSummary.addEventListener('click', (e) => {
    if (e.target.closest('[data-action="models"]')) openConfig();
  });

  function renderPersistInfo(st) {
    if (!st?.enabled) {
      el.persistInfo.removeAttribute('data-error');
      el.persistInfo.textContent = 'Persistência desativada: alterações valem até reiniciar o servidor.';
    } else if (st.error) {
      el.persistInfo.dataset.error = '';
      el.persistInfo.textContent = `Não foi possível salvar em ${st.path}: ${st.error}`;
    } else {
      el.persistInfo.removeAttribute('data-error');
      el.persistInfo.innerHTML = `Salvo em <code>${esc(st.path)}</code>${st.saved_at ? ` às ${esc(fmtTime(st.saved_at))}` : ''}`;
    }
    el.persistInfo.title = el.persistInfo.textContent;
    if (st?.error && st.error !== lastPersistError) toast(`Configuração aplicada, mas não salva: ${st.error}`, 'error');
    lastPersistError = st?.error || '';
  }

  // ---------- config dialog ----------
  function modelSelect(p, name, label, value) {
    const listed = p.models.filter((m) => !m.hidden);
    const known = listed.some((m) => m.id === value);
    const id = `${name}-${p.id}`;
    const options = listed.map((m) =>
      `<option value="${esc(m.id)}"${m.id === value ? ' selected' : ''}>${esc(m.label)} — ${esc(m.id)}</option>`).join('');
    return `
      <div class="field model-select">
        <label for="${esc(id)}">${esc(label)}</label>
        <select id="${esc(id)}" data-model="${esc(name)}">
          ${options}
          <option value="${CUSTOM}"${known ? '' : ' selected'}>Outro (ID personalizado)…</option>
        </select>
        <input type="text" data-custom="${esc(name)}" value="${known ? '' : esc(value)}" placeholder="ID do modelo"
               aria-label="${esc(label)} personalizado" autocomplete="off" spellcheck="false"${known ? ' hidden' : ''}>
        <p class="note" data-tone="warn" data-unknown hidden></p>
      </div>`;
  }

  // Selected model ID for one tier inside a provider fieldset.
  function selectedModel(fs, name) {
    const sel = fs.querySelector(`select[data-model="${name}"]`);
    return sel.value === CUSTOM ? fs.querySelector(`[data-custom="${name}"]`).value.trim() : sel.value;
  }

  // Keeps the effort list to levels both selected models support and flags
  // models missing from the CLI's live catalog.
  function syncProviderFields(fs) {
    const p = snapshot.providers.find((x) => x.id === fs.dataset.id);
    if (!p) return;
    const chosen = ['heavy_model', 'std_model'].map((n) => selectedModel(fs, n));
    let efforts = p.efforts;
    for (const id of chosen) {
      const m = p.models.find((x) => x.id === id);
      if (m?.efforts?.length) efforts = efforts.filter((e) => m.efforts.includes(e));
    }
    const effortSel = fs.querySelector('[name="effort"]');
    const current = effortSel.value || p.effort;
    effortSel.innerHTML = efforts.map((e) => `<option value="${esc(e)}"${e === current ? ' selected' : ''}>${esc(e)}</option>`).join('');
    if (!efforts.includes(current) && efforts.length) effortSel.value = efforts.includes('medium') ? 'medium' : efforts[0];

    ['heavy_model', 'std_model'].forEach((n, i) => {
      const warn = fs.querySelector(`select[data-model="${n}"]`).parentElement.querySelector('[data-unknown]');
      const id = chosen[i];
      const missing = p.catalog && id && !p.models.some((m) => m.id === id);
      warn.hidden = !missing;
      warn.textContent = missing ? `"${id}" não está no catálogo do ${p.name}; a execução pode falhar.` : '';
    });
  }

  function openConfig() {
    if (!snapshot) return;
    el.configError.hidden = true;
    el.cfgDefault.innerHTML = snapshot.providers.map((p) =>
      `<option value="${esc(p.id)}"${p.id === snapshot.config.default_provider ? ' selected' : ''}>${esc(p.name)}</option>`).join('');
    el.cfgFailover.checked = snapshot.config.failover;
    el.configFields.innerHTML = snapshot.providers.map((p) => {
      const efforts = '';
      return `
        <fieldset class="config-provider" data-id="${esc(p.id)}">
          <legend>${esc(p.name)}<code>${esc(p.binary)}</code>${p.default ? ' <span class="chip-default">Padrão</span>' : ''}</legend>
          <p class="note">${p.catalog ? `Modelos disponíveis no CLI instalado (<code>${esc(p.catalog)}</code>)` : 'Lista interna: o CLI não informou um catálogo de modelos.'}</p>
          ${modelSelect(p, 'heavy_model', 'Modelo heavy (arquitetura, segurança, review)', p.heavy_model)}
          ${modelSelect(p, 'std_model', 'Modelo standard (discovery, spec, implementação, E2E)', p.std_model)}
          <div class="field">
            <label for="effort-${esc(p.id)}">Nível de esforço</label>
            <select id="effort-${esc(p.id)}" name="effort">${efforts}</select>
          </div>
        </fieldset>`;
    }).join('');
    el.configFields.querySelectorAll('.config-provider').forEach(syncProviderFields);
    el.configDialog.showModal();
  }

  el.configBtn.addEventListener('click', openConfig);
  el.configFields.addEventListener('change', (e) => {
    const sel = e.target.closest('select[data-model]');
    if (!sel) return;
    const input = sel.parentElement.querySelector('[data-custom]');
    input.hidden = sel.value !== CUSTOM;
    if (!input.hidden) input.focus();
  });
  el.configFields.addEventListener('change', (e) => {
    const fs = e.target.closest('.config-provider');
    if (fs && (e.target.matches('select[data-model]') || e.target.matches('[data-custom]'))) syncProviderFields(fs);
  });
  el.configDialog.querySelectorAll('[data-close]').forEach((b) => b.addEventListener('click', () => el.configDialog.close()));
  el.configDialog.addEventListener('click', (e) => { if (e.target === el.configDialog) el.configDialog.close(); });

  el.configForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    const providers = {};
    let missing = null;
    el.configFields.querySelectorAll('.config-provider').forEach((fs) => {
      const pick = (name) => {
        const sel = fs.querySelector(`select[data-model="${name}"]`);
        const value = sel.value === CUSTOM ? fs.querySelector(`[data-custom="${name}"]`).value.trim() : sel.value;
        if (!value) missing ??= fs.querySelector('legend').firstChild.textContent;
        return value;
      };
      providers[fs.dataset.id] = {
        heavy_model: pick('heavy_model'),
        std_model: pick('std_model'),
        effort: fs.querySelector('[name="effort"]').value,
      };
    });
    if (missing) {
      el.configError.textContent = `Informe o ID do modelo personalizado de ${missing}.`;
      el.configError.hidden = false;
      return;
    }
    try {
      await api('PUT', '/api/config', { default_provider: el.cfgDefault.value, failover: el.cfgFailover.checked, providers });
      el.configDialog.close();
      toast('Configuração salva.', 'success');
    } catch (err) {
      el.configError.textContent = err.message;
      el.configError.hidden = false;
    }
  });

  // ---------- state ----------
  function renderControls() {
    const running = Boolean(snapshot?.run.running);
    const hasRepo = Boolean(snapshot?.workspace?.path);
    el.startBtn.disabled = running || busy || !hasRepo;
    el.startBtn.title = hasRepo ? '' : 'Configure o repositório primeiro';
    el.repoSaveBtn.disabled = running;
    el.stopBtn.disabled = !running;
    el.startLabel.textContent = running ? 'Em execução…' : 'Iniciar execução';
  }

  function applySnapshot(data) {
    snapshot = data;
    el.version.textContent = data.version;
    if (!modeTouched && !data.run.running) setMode(data.dry_run ? 'dry' : 'real');
    if (data.run.started_at !== lastRunStart) {
      lastRunStart = data.run.started_at;
      viewStory = null;
      logsClearedAt = null;
    }
    renderWorkspace(data.workspace || {});
    renderQuality(data.workspace || {});
    renderProviders(data.providers);
    renderProviderPicker(data);
    renderPersistInfo(data.settings);
    renderRun(data.run);
    renderLogs(data.run.logs || []);
    renderControls();
    tick();
  }

  // Live counters (elapsed time and cooldowns) between server pushes.
  function tick() {
    const now = Date.now();
    document.querySelectorAll('[data-elapsed]').forEach((node) => {
      const end = node.dataset.end ? Date.parse(node.dataset.end) : now;
      node.textContent = fmtDuration(end - Date.parse(node.dataset.elapsed));
    });
    let expired = false;
    document.querySelectorAll('[data-countdown]').forEach((node) => {
      const left = Date.parse(node.dataset.countdown) - now;
      node.textContent = fmtCountdown(left);
      if (left <= 0) expired = true;
    });
    if (expired && !refetchScheduled) {
      refetchScheduled = true;
      setTimeout(() => { refetchScheduled = false; fetchStatus(); }, 600);
    }
  }
  setInterval(tick, 1000);

  async function fetchStatus() {
    try {
      applySnapshot(await api('GET', '/api/status'));
      return true;
    } catch {
      setConn('offline', 'Sem conexão');
      return false;
    }
  }

  function setConn(state, text) {
    el.conn.dataset.state = state;
    el.connText.textContent = text;
  }

  function startPolling() {
    if (pollTimer) return;
    pollTimer = setInterval(async () => {
      if (await fetchStatus()) {
        if (snapshot?.desktop) setConn('live', 'Aplicativo local');
        else setConn('polling', 'Reconectando…');
      }
    }, POLL_MS);
  }
  function stopPolling() {
    clearInterval(pollTimer);
    pollTimer = null;
  }

  function connect() {
    if (!('EventSource' in window)) {
      startPolling();
      return;
    }
    const source = new EventSource('/api/events');
    source.onopen = () => {
      stopPolling();
      setConn('live', 'Ao vivo');
    };
    source.onmessage = (e) => {
      try { applySnapshot(JSON.parse(e.data)); } catch (err) { console.error('SSE inválido', err); }
    };
    source.onerror = () => {
      setConn('polling', 'Reconectando…');
      startPolling();
    };
  }

  async function boot() {
    updateModeNote();
    await fetchStatus();
    if (snapshot?.desktop) {
      setConn('live', 'Aplicativo local');
      startPolling();
      return;
    }
    connect();
  }

  boot();
})();
