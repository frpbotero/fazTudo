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

  const RUN_RESULT = {
    succeeded: { label: 'Concluído', tone: 'success' },
    failed: { label: 'Falhou', tone: 'danger' },
    canceled: { label: 'Cancelado', tone: 'warn' },
  };
  // Validation is apart from the result: a run that finished may still have
  // delivered nothing approved.
  const VALIDATION = {
    passed: { label: 'Validação aprovada', short: 'aprovada', tone: 'success' },
    failed: { label: 'Validação reprovada', short: 'reprovada', tone: 'danger' },
    inconclusive: { label: 'Validação inconclusiva', short: 'inconclusiva', tone: 'warn' },
    not_evaluated: { label: 'Não avaliada', short: 'não avaliada', tone: 'neutral' },
  };
  const INTEGRATION = {
    integrated: { label: 'Na branch base', tone: 'success' },
    blocked: { label: 'Fora da base', tone: 'danger' },
    partial: { label: 'Integração parcial', tone: 'warn' },
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
    modeNote: $('modeNote'), startBtn: $('startBtn'), startLabel: $('startLabel'), stopBtn: $('stopBtn'), resumeBtn: $('resumeBtn'), reconcileBtn: $('reconcileBtn'),
    repoForm: $('repoForm'), repoPath: $('repoPath'), repoSaveBtn: $('repoSaveBtn'),
    discovery: $('discovery'), discoveryBadge: $('discoveryBadge'),
    openMdBtn: $('openMdBtn'), mdFile: $('mdFile'), fileInfo: $('fileInfo'),
    storyDialog: $('storyDialog'), storyForm: $('storyForm'), storyDialogTitle: $('storyDialogTitle'),
    storyModeNote: $('storyModeNote'), storyList: $('storyList'), storySelCount: $('storySelCount'),
    storyBulk: $('storyBulk'), storyPreamble: $('storyPreamble'), storyError: $('storyError'),
    storyStartBtn: $('storyStartBtn'), storyStartLabel: $('storyStartLabel'),
    sidebar: $('sidebar'), menuBtn: $('menuBtn'), scopeName: $('scopeName'), discoveryDot: $('discoveryDot'),
    navLive: $('navLive'), navIssues: $('navIssues'), crumbRepo: $('crumbRepo'), crumbView: $('crumbView'),
    topRun: $('topRun'), topElapsed: $('topElapsed'), runStats: $('runStats'), runEmpty: $('runEmpty'),
    waveTabs: $('waveTabs'), waveHead: $('waveHead'), lanes: $('lanes'), storyPanel: $('storyPanel'),
    storyHead: $('storyHead'), newRunLink: $('newRunLink'), qualityEditBtn: $('qualityEditBtn'),
    providerPicker: $('providerPicker'), modelSummary: $('modelSummary'), failoverToggle: $('failoverToggle'),
    cfgDefault: $('cfgDefault'), cfgFailover: $('cfgFailover'), persistInfo: $('persistInfo'),
    providers: $('providers'), restoreAllBtn: $('restoreAllBtn'),
    runMeta: $('runMeta'), runBadge: $('runBadge'), progress: $('progress'), progressBar: $('progressBar'), steps: $('steps'),
    log: $('log'), autoScroll: $('autoScroll'), clearLogsBtn: $('clearLogsBtn'),
    cleanupCard: $('cleanupCard'), healthForm: $('healthForm'), healthArea: $('healthArea'),
    healthComments: $('healthComments'), healthTests: $('healthTests'), healthRounds: $('healthRounds'),
    healthStartBtn: $('healthStartBtn'), healthSuggestBtn: $('healthSuggestBtn'), healthAreas: $('healthAreas'),
    healthWarnings: $('healthWarnings'),
    configDialog: $('configDialog'), configForm: $('configForm'), configFields: $('configFields'), configError: $('configError'),
    quality: $('quality'), qualityDialog: $('qualityDialog'), qualityForm: $('qualityForm'), qualityRepo: $('qualityRepo'),
    qAuto: $('qAuto'), qSuggested: $('qSuggested'), qCustom: $('qCustom'), qChecks: $('qChecks'), qProtected: $('qProtected'),
    qMigrationChecks: $('qMigrationChecks'), qBenchmarkChecks: $('qBenchmarkChecks'),
    qProfiles: $('qProfiles'), qProfilesNote: $('qProfilesNote'),
    qMaxLine: $('qMaxLine'), qTimeout: $('qTimeout'), qCandidates: $('qCandidates'), qMode: $('qMode'), qFlow: $('qFlow'), qFlowNote: $('qFlowNote'), qStages: $('qStages'), qGit: $('qGit'), qMaxTurns: $('qMaxTurns'), qStopOnTurnLimit: $('qStopOnTurnLimit'), qPromoteUnverified: $('qPromoteUnverified'), qStrictCommands: $('qStrictCommands'), qAgentCommands: $('qAgentCommands'), qEphemeralStacks: $('qEphemeralStacks'),
    promptsError: $('promptsError'), promptList: $('promptList'), promptForm: $('promptForm'), promptTitle: $('promptTitle'),
    promptDescription: $('promptDescription'), promptParts: $('promptParts'), promptWarning: $('promptWarning'), promptValue: $('promptValue'),
    promptDefault: $('promptDefault'), promptContractBox: $('promptContractBox'), promptContract: $('promptContract'), promptDiscard: $('promptDiscard'),
    promptDiscardBtn: $('promptDiscardBtn'), promptKeepBtn: $('promptKeepBtn'), promptStatus: $('promptStatus'), promptResetBtn: $('promptResetBtn'), qualityError: $('qualityError'),
    branches: $('branches'), branchesRefreshBtn: $('branchesRefreshBtn'),
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
  const pill = ({ label, tone }) => `<span class="pill" data-tone="${tone}">${esc(label)}</span>`;
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
      userClosed.clear();
      viewStory = null;
      viewWave = null;
      location.hash = '#execucao';
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
      ? 'Uma análise de dependências agrupa as US em ondas: as independentes rodam em paralelo, cada uma na sua branch; as demais esperam as que precisam. Uma falha interrompe a execução.'
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

  // Continues a failed, stopped or app-closed run from the step where it stopped, on the story's own branch.
  el.resumeBtn.addEventListener('click', async () => {
    el.resumeBtn.disabled = true;
    try {
      await api('POST', '/api/pipeline/resume');
      openDetails.clear();
      userClosed.clear();
      viewStory = null;
      viewWave = null;
      toast('Execução retomada de onde parou.', 'success');
    } catch (err) {
      toast(err.message, 'error');
    } finally {
      el.resumeBtn.disabled = false;
    }
  });

  // Completes a promotion that reached only some repositories; nothing is reset.
  el.reconcileBtn.addEventListener('click', async () => {
    el.reconcileBtn.disabled = true;
    try {
      await api('POST', '/api/pipeline/reconcile');
      toast('Integração reconciliada em todos os repositórios.', 'success');
    } catch (err) {
      toast(err.message, 'error');
    } finally {
      el.reconcileBtn.disabled = false;
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

  function setDiscovery(tone, text) {
    el.discoveryDot.dataset.tone = tone;
    el.discoveryBadge.textContent = text;
  }

  function repoName(path) {
    return path ? path.split('/').filter(Boolean).pop() || path : '';
  }

  function renderWorkspace(ws) {
    if (!repoDirty && document.activeElement !== el.repoPath) el.repoPath.value = ws.path || '';
    el.scopeName.textContent = repoName(ws.path) || 'Não configurado';
    el.scopeName.title = ws.path || '';
    el.crumbRepo.textContent = repoName(ws.path) || 'FazTudo';

    if (!ws.path) {
      setDiscovery('neutral', 'Configure o caminho');
      el.discovery.innerHTML = '<p class="note">Configure o repositório para habilitar as execuções.</p>';
      return;
    }
    const d = ws.discovery;
    if (!d) {
      setDiscovery('warn', 'Discovery pendente');
      el.discovery.innerHTML = '<p class="note">O discovery das stacks será feito na primeira execução desta sessão e reutilizado nas seguintes.</p>';
      return;
    }

    setDiscovery('success', `Discovery às ${fmtTime(d.created_at).slice(0, 5)}`);
    const groups = new Map();
    for (const s of d.stacks || []) {
      if (!groups.has(s.dir)) groups.set(s.dir, []);
      groups.get(s.dir).push(s);
    }
    const stacks = groups.size
      ? [...groups].map(([dir, items]) => `
          <div class="stack-group">
            <span class="stack-dir" title="${esc(dir)}">${esc(dir === '.' ? '(raiz)' : `${dir}/`)}</span>
            <span class="chips">${items.map((s) => `<span class="chip" title="${esc(s.evidence)}">${esc(s.name)}</span>`).join('')}</span>
          </div>`).join('')
      : '<p class="note">Nenhum manifesto reconhecido; veja o relatório na etapa de discovery.</p>';
    const via = [d.provider, d.model].filter(Boolean).join(' · ');
    const origin = d.origin === 'incremental' ? 'Atualizado' : 'Feito';
    const files = d.fingerprint ? Object.keys(d.fingerprint.files || {}).length : 0;
    // Validity comes from the fingerprint: an old report without one is
    // remade on the next run, so the panel says so instead of vouching for it.
    const validity = !d.fingerprint
      ? 'Sem impressão digital dos arquivos: será refeito na próxima execução.'
      : `Válido enquanto ${files} arquivo(s) de manifesto, dependência, instrução e contrato não mudarem${d.validated_at ? `; conferido às ${esc(fmtTime(d.validated_at))}` : ''}.`;
    el.discovery.innerHTML = `
      <div class="discovery-meta">
        <span>${origin} às ${esc(fmtTime(d.created_at))}${via ? ` via ${esc(via)}` : ''}${d.simulated ? ' (simulado)' : ''}</span>
        <button class="btn btn-ghost btn-sm" type="button" data-action="rediscover">${icon('rotate')}Refazer</button>
      </div>
      ${d.reason ? `<p class="note">Motivo: ${esc(d.reason)}.</p>` : ''}
      <p class="note">${validity}</p>
      <div class="stack-groups">${stacks}</div>`;
  }

  // ---------- quality gates ----------
  // A command line is "name: run", or "name [format:path]: run" when the
  // command writes a per-test report.
  const reportTag = (r) => (r?.format ? ` [${r.format}${r.path ? `:${r.path}` : ''}]` : '');
  const commandLines = (checks) => (checks || []).map((c) => `${c.name}${reportTag(c.report)}: ${c.run}`).join('\n');

  function parseCommands(text) {
    return text.split('\n').map((l) => l.trim()).filter(Boolean).map((line) => {
      const i = line.indexOf(': ');
      if (i <= 0) return { name: line, run: line };
      const cmd = { name: line.slice(0, i).trim(), run: line.slice(i + 2).trim() };
      const m = cmd.name.match(/^(.*?)\s*\[([\w-]+)(?::([^\]]+))?\]$/);
      if (m) {
        cmd.name = m[1].trim() || cmd.run;
        cmd.report = { format: m[2], ...(m[3] ? { path: m[3].trim() } : {}) };
      }
      return cmd;
    });
  }

  const FLOW_LABELS = {
    full: 'completo: todas as etapas',
    balanced: 'equilibrado: sem segurança, E2E e documentação',
    fast: 'rápido: Passo 0, plano de testes, arquiteto, devs e verificação',
    audit: 'auditoria: testes, code review e segurança do código existente',
  };
  const FLOW_OFF = {
    full: [],
    balanced: ['security', 'e2e', 'docs'],
    fast: ['security', 'review', 'e2e', 'acceptance', 'docs'],
    audit: ['context', 'spec', 'arch', 'frontend', 'backend', 'e2e', 'docs'],
  };
  const stageBoxes = () => [...el.qStages.querySelectorAll('input[data-stage]')];

  function flowLabel(p) {
    if (p.flow !== 'custom') return FLOW_LABELS[p.flow] || FLOW_LABELS.full;
    const off = p.disabled_stages || [];
    return off.length ? `personalizado: sem ${off.join(', ')}` : 'personalizado: todas as etapas';
  }

  // Presets show their stages ticked or not; only "custom" can be edited.
  function syncFlowForm() {
    const flow = el.qFlow.value;
    el.qStages.hidden = flow !== 'custom';
    el.qFlowNote.textContent = flow === 'custom' ? '' : `Etapas desligadas: ${FLOW_OFF[flow].join(', ') || 'nenhuma'}.`;
  }

  function renderQuality(ws) {
    el.qualityEditBtn.disabled = !ws.path;
    if (!ws.path) {
      el.quality.innerHTML = '<p class="note">Configure o repositório para editar os portões.</p>';
      return;
    }
    const p = ws.policy || {};
    const checks = p.auto_checks ? ws.suggested || [] : p.checks || [];
    const origin = p.auto_checks ? 'detectados' : 'personalizados';
    const chips = checks.length
      ? checks.map((c) => `<span class="chip" title="${esc(c.run)}">${esc(c.name)}</span>`).join('')
      : `<span class="note" data-tone="warn">Nenhum comando ${p.auto_checks ? 'detectado' : 'configurado'}: apenas checagens estáticas.</span>`;
    const protectedList = (p.protected || []).map((x) => `<code>${esc(x)}</code>`).join(' ');
    el.quality.innerHTML = `
      <dl class="kv">
        <dt>Comandos</dt><dd class="wrap">${chips}<span class="note">${esc(origin)}</span></dd>
        <dt>Protegidos</dt><dd class="wrap">${protectedList || '<span class="note">nenhum</span>'}</dd>
        <dt>Linhas</dt><dd>até ${esc(p.max_line_length)} caracteres</dd>
        <dt>Git</dt><dd>${p.git ? 'branch por US, commit por etapa' : 'desativado'}</dd>
        <dt>Desenvolvimento</dt><dd>${p.sequential ? 'sequencial: uma US de cada vez' : 'paralelo: US e frentes independentes juntas'}</dd>
        <dt>Fluxo</dt><dd>${esc(flowLabel(p))}</dd>
        <dt>Candidatos</dt><dd>${p.candidates > 1 ? `best-of-${esc(p.candidates)}` : '1 por implementação'}</dd>
        <dt>Turnos</dt><dd>${p.max_turns ? `até ${esc(p.max_turns)} por chamada` : 'padrão (60 / 30)'}${p.stop_on_turn_limit ? '; para ao esgotar' : ''}</dd>
        <dt>Shell liberado</dt><dd class="wrap">${(p.agent_commands || []).map((x) => `<code>${esc(x)}</code>`).join(' ') || '<span class="note">só verificação e git de leitura</span>'}</dd>
        <dt>Stacks de teste</dt><dd class="wrap">${(p.ephemeral_stacks || []).map((x) => `<code>${esc(x)}</code>`).join(' ') || '<span class="note">limpeza desligada</span>'}</dd>
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
    el.qProfiles.value = (p.profiles || []).join(', ');
    const suggestedProfiles = (snapshot?.workspace?.suggested_profiles || []).filter((x) => !(p.profiles || []).includes(x));
    el.qProfilesNote.textContent = 'Convenções da stack para implementadores e revisores. Opcionais: só valem os listados aqui.' +
      (suggestedProfiles.length ? ` Detectado neste repositório: ${suggestedProfiles.join(', ')}.` : '');
    el.qMigrationChecks.value = commandLines(p.migration_checks);
    el.qBenchmarkChecks.value = commandLines(p.benchmark_checks);
    el.qMaxLine.value = p.max_line_length;
    el.qTimeout.value = p.check_timeout_s;
    el.qCandidates.value = String(p.candidates || 1);
    el.qMode.value = p.sequential ? 'sequential' : 'parallel';
    el.qGit.checked = p.git;
    el.qMaxTurns.value = p.max_turns || '';
    el.qStopOnTurnLimit.checked = !!p.stop_on_turn_limit;
    el.qPromoteUnverified.checked = !!p.promote_unverified;
    el.qStrictCommands.checked = !!p.strict_commands;
    el.qAgentCommands.value = (p.agent_commands || []).join('\n');
    el.qEphemeralStacks.value = (p.ephemeral_stacks || []).join('\n');
    el.qFlow.value = p.flow || 'full';
    const off = p.disabled_stages || [];
    stageBoxes().forEach((b) => { b.checked = !off.includes(b.dataset.stage); });
    syncFlowForm();
    syncQualityForm();
    el.qualityDialog.showModal();
  }

  el.qualityEditBtn.addEventListener('click', openQuality);
  el.qAuto.addEventListener('change', syncQualityForm);
  el.qFlow.addEventListener('change', syncFlowForm);
  el.qualityDialog.querySelectorAll('[data-close]').forEach((b) => b.addEventListener('click', () => el.qualityDialog.close()));
  el.qualityDialog.addEventListener('click', (e) => { if (e.target === el.qualityDialog) el.qualityDialog.close(); });

  el.qualityForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    const policy = {
      auto_checks: el.qAuto.checked,
      checks: parseCommands(el.qChecks.value),
      profiles: el.qProfiles.value.split(',').map((x) => x.trim()).filter(Boolean),
      migration_checks: parseCommands(el.qMigrationChecks.value),
      benchmark_checks: parseCommands(el.qBenchmarkChecks.value),
      protected: el.qProtected.value.split('\n').map((l) => l.trim()).filter(Boolean),
      git: el.qGit.checked,
      max_line_length: Number(el.qMaxLine.value) || 0,
      check_timeout_s: Number(el.qTimeout.value) || 0,
      candidates: Number(el.qCandidates.value) || 1,
      sequential: el.qMode.value === 'sequential',
      flow: el.qFlow.value,
      disabled_stages: stageBoxes().filter((b) => !b.checked).map((b) => b.dataset.stage),
      parallel_lenses: snapshot?.workspace?.policy?.parallel_lenses,
      max_fix_rounds: snapshot?.workspace?.policy?.max_fix_rounds,
      max_turns: Number(el.qMaxTurns.value) || 0,
      stop_on_turn_limit: el.qStopOnTurnLimit.checked,
      agent_commands: el.qAgentCommands.value.split('\n').map((l) => l.trim()).filter(Boolean),
      ephemeral_stacks: el.qEphemeralStacks.value.split('\n').map((l) => l.trim()).filter(Boolean),
      base_branches: snapshot?.workspace?.policy?.base_branches,
      promote_unverified: el.qPromoteUnverified.checked,
      strict_commands: el.qStrictCommands.checked,
      risk_rules: snapshot?.workspace?.policy?.risk_rules,
      skip_baseline: snapshot?.workspace?.policy?.skip_baseline,
      style_blocking: snapshot?.workspace?.policy?.style_blocking,
      max_parallel_reviews: snapshot?.workspace?.policy?.max_parallel_reviews,
      review_provider: snapshot?.workspace?.policy?.review_provider,
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

  // ---------- base branches ----------
  let repos = null; // the repository and its nested ones, with their branches
  let reposPath = null; // repository they were listed for
  let branchesKey = '';

  async function loadRepos() {
    reposPath = snapshot?.workspace?.path || null;
    repos = null;
    if (reposPath) {
      try {
        repos = await api('GET', '/api/workspace/repos');
      } catch (err) {
        repos = { error: err.message };
      }
    }
    renderBranches(snapshot?.workspace || {});
  }

  function branchOptions(repo, base) {
    const names = repo.branches || [];
    const options = [`<option value="">Atual (${esc(repo.current || 'sem branch')})</option>`];
    if (base && !names.includes(base)) options.push(`<option value="${esc(base)}" selected>${esc(base)} (não encontrada)</option>`);
    for (const b of names) options.push(`<option value="${esc(b)}"${b === base ? ' selected' : ''}>${esc(b)}</option>`);
    return options.join('');
  }

  function branchesHTML(ws) {
    if (!ws.path) return '<p class="note">Configure o repositório para escolher a branch base.</p>';
    if (!repos) return '<p class="note">Carregando…</p>';
    if (repos.error) return `<p class="note" data-tone="warn">${esc(repos.error)}</p>`;
    if (!repos.length) return '<p class="note">O repositório ainda não usa git; a primeira execução o inicializa.</p>';
    const bases = ws.policy?.base_branches || {};
    const rows = repos.map((r) => `
      <dt><label for="base-${esc(r.path)}">${esc(r.path === '.' ? '(raiz)' : `${r.path}/`)}</label></dt>
      <dd><select id="base-${esc(r.path)}" data-repo="${esc(r.path)}">${branchOptions(r, bases[r.path])}</select></dd>`).join('');
    const note = ws.policy?.git
      ? 'Cada execução nova parte dessas branches; as branches faztudo/ saem delas.'
      : 'Git desativado nos portões de qualidade: a branch base não é usada.';
    return `<dl class="kv">${rows}</dl><p class="note">${note}</p>`;
  }

  function renderBranches(ws) {
    if ((ws.path || null) !== reposPath) {
      loadRepos();
      return;
    }
    // Snapshots arrive continuously; redraw only on change so an open select stays open.
    const html = branchesHTML(ws);
    if (html === branchesKey) return;
    branchesKey = html;
    el.branches.innerHTML = html;
  }

  el.branchesRefreshBtn.addEventListener('click', loadRepos);
  el.branches.addEventListener('change', async (e) => {
    const select = e.target.closest('select[data-repo]');
    const policy = snapshot?.workspace?.policy;
    if (!select || !policy) return;
    const bases = { ...policy.base_branches, [select.dataset.repo]: select.value };
    try {
      await api('PUT', '/api/workspace/policy', { ...policy, base_branches: bases });
      toast('Branch base salva.', 'success');
    } catch (err) {
      toast(err.message, 'error');
      branchesKey = '';
      renderBranches(snapshot.workspace);
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

  // What the adapter enforces, in one line: users choose a provider knowing
  // which restrictions hold for it.
  function capabilitySummary(c) {
    if (!c) return '';
    const parts = [c.command_allowlist ? 'comandos restritos à lista' : 'comandos não restritos à lista'];
    if (!c.max_turns) parts.push('sem limite de turnos');
    if (!c.diagnose) parts.push('diagnóstico só leitura');
    return `${parts.join(' · ')} · isolamento: ${c.isolation}`;
  }

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
            ${pill(status)}
          </div>
          <dl class="kv">
            <dt>Heavy</dt><dd title="${esc(p.heavy_model)}">${esc(p.heavy_model)}</dd>
            <dt>Standard</dt><dd title="${esc(p.std_model)}">${esc(p.std_model)}</dd>
            <dt>Esforço</dt><dd>${esc(p.effort || 'padrão')}</dd>
            ${p.version ? `<dt>Versão</dt><dd title="${esc(p.version)}">${esc(p.version)}</dd>` : ''}
            <dt>Garantias</dt><dd title="${esc(capabilitySummary(p.capabilities))}">${esc(capabilitySummary(p.capabilities))}</dd>
          </dl>
          <div class="provider-foot">${foot}</div>
        </div>`;
    }).join('');
  }

  // ---------- pipeline ----------
  // Open <details> survive the re-render every pipeline event triggers. A
  // step the user never touched opens by default only when it failed.
  const userClosed = new Set();
  let viewWave = null; // wave tab pinned by the user (0 = preparation); null follows the run
  let lastShownWave = null;

  function panelKey(node, selector) {
    const owner = node.closest('[data-key]');
    if (!owner) return '';
    return `${owner.dataset.key}|${selector}|${[...owner.querySelectorAll(selector)].indexOf(node)}`;
  }
  el.storyPanel.addEventListener('toggle', (e) => {
    if (e.target.tagName !== 'DETAILS') return;
    const key = panelKey(e.target, 'details');
    if (!key) return;
    if (e.target.open) { openDetails.add(key); userClosed.delete(key); } else { openDetails.delete(key); userClosed.add(key); }
  }, true);
  el.storyPanel.addEventListener('scroll', (e) => {
    if (e.target.tagName !== 'PRE') return;
    const key = panelKey(e.target, 'pre');
    if (key) scrolled.set(key, e.target.scrollTop);
  }, true);
  el.storyPanel.addEventListener('click', async (e) => {
    const retry = e.target.closest('button[data-retry-step]');
    if (retry) {
      e.preventDefault();
      retry.disabled = true;
      try {
        await api('POST', '/api/pipeline/retry', {
          story: Number(retry.dataset.retryStory),
          step: retry.dataset.retryStep,
        });
        openDetails.clear();
        userClosed.clear();
        viewStory = null;
        viewWave = null;
        toast('Etapa reiniciada. As validações seguintes serão executadas novamente.', 'success');
      } catch (err) {
        retry.disabled = false;
        toast(err.message, 'error');
      }
      return;
    }
    const diff = e.target.closest('button[data-diff]');
    if (diff) {
      e.preventDefault();
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
  el.waveTabs.addEventListener('click', (e) => {
    const tab = e.target.closest('[data-wave]');
    if (!tab) return;
    const wave = Number(tab.dataset.wave);
    viewWave = wave === currentWave(snapshot.run, runWaves(snapshot.run)) ? null : wave;
    viewStory = null;
    renderRun(snapshot.run);
  });
  el.lanes.addEventListener('click', (e) => {
    const lane = e.target.closest('[data-story]');
    if (!lane) return;
    viewStory = Number(lane.dataset.story);
    renderRun(snapshot.run);
  });

  const STORY_STATUS = {
    pending: { label: 'Aguardando', tone: 'neutral' },
    running: { label: 'Em execução', tone: 'accent' },
    done: { label: 'Entregue', tone: 'success' },
    warning: { label: 'Com pendências', tone: 'warn' },
    skipped: { label: 'Bloqueada', tone: 'warn' },
    failed: { label: 'Falhou', tone: 'danger' },
    canceled: { label: 'Cancelada', tone: 'neutral' },
    waiting_input: { label: 'Aguardando decisão', tone: 'warn' },
  };
  const CRITERION_LABEL = { existente: 'existente', parcial: 'parcial', ausente: 'ausente', ambiguo: 'ambíguo' };
  const KIND_TAGS = { fix: 'Correção', verify: 'Verificação', investigate: 'Investigação', stream: 'Frente', merge: 'Integração' };
  const CAUSE_STATUS = {
    open: { label: 'Em correção', sev: 'média' },
    fixed: { label: 'Corrigida', sev: 'baixa' },
    pending: { label: 'Pendente', sev: 'alta' },
    blocked: { label: 'Bloqueada', sev: 'alta' },
    inherited: { label: 'Herdada', sev: 'média' },
    infra: { label: 'Infraestrutura', sev: 'média' },
    unverified: { label: 'Não reverificada', sev: 'média' },
  };
  const PLAN_SOURCE = {
    architect: '',
    heuristic: 'Simulação: escolha por palavras-chave da US.',
    fallback: 'O arquiteto não retornou um plano; todos os agentes foram executados.',
  };
  const finished = (s) => ['done', 'skipped', 'omitted', 'warning'].includes(s.status);
  // Canceled steps are hidden, except the one an interrupted run resumes from.
  const isHidden = (s) => (s.status === 'canceled' && !s.stoppedHere) || s.status === 'omitted';
  const isVerifier = (s) => /^checks(-verify-\d+)?$/.test(s.id);
  const storyLabel = (st, i) => st.id || `#${i + 1}`;
  // The title without the ID it usually starts with ("US-B01 — Modelar…").
  const storyName = (st) => (st.id ? st.title.replace(st.id, '').replace(/^[\s—–:.-]+/, '') : st.title) || st.title;

  function stepMarker(s) {
    switch (s.status) {
      case 'running': return '<span class="spinner" aria-hidden="true"></span>';
      case 'done':
      case 'skipped': return icon('check');
      case 'failed': return icon('x');
      case 'warning': return icon('alert');
      case 'canceled': return icon('ban');
      case 'omitted': return icon('minus');
      default: return s.kind === 'fix' ? icon('wrench') : '<span class="dot" aria-hidden="true"></span>';
    }
  }

  // ----- waves -----
  function runWaves(run) {
    const list = run.stories || [];
    if (run.waves?.length) return run.waves;
    return list.length === 1 ? [[0]] : [];
  }

  // The wave the run is in: 0 while preparing, n for waves[n - 1].
  function currentWave(run, waves) {
    if ((run.current ?? -1) < 0) return 0;
    const at = waves.findIndex((w) => w.includes(run.current));
    return at < 0 ? 0 : at + 1;
  }

  function groupStatus(statuses) {
    if (statuses.includes('running')) return 'running';
    if (statuses.includes('failed')) return 'failed';
    if (statuses.length && statuses.every((s) => ['done', 'warning', 'skipped'].includes(s))) {
      return statuses.some((s) => s !== 'done') ? 'warning' : 'done';
    }
    return 'pending';
  }

  function tabMark(status) {
    switch (status) {
      case 'running': return '<span class="spinner" aria-hidden="true"></span>';
      case 'done': return icon('check');
      case 'failed': return icon('x');
      case 'warning': return icon('alert');
      default: return '<span class="dot" aria-hidden="true"></span>';
    }
  }

  function renderWaveTabs(run, waves, shown) {
    const list = run.stories || [];
    const prep = [run.discovery, run.analysis].filter(Boolean).map((s) => (s.status === 'skipped' ? 'done' : s.status));
    const tabs = [`
      <button type="button" class="wave-tab" role="tab" data-wave="0" data-status="${groupStatus(prep)}" aria-selected="${shown === 0}">
        ${tabMark(groupStatus(prep))}<span>Preparação</span>
      </button>`];
    waves.forEach((w, n) => {
      const statuses = w.map((i) => list[i]?.status || 'pending');
      const status = groupStatus(statuses);
      const delivered = statuses.filter((s) => s === 'done' || s === 'warning').length;
      const label = waves.length === 1 ? 'Desenvolvimento' : `Onda ${n + 1}`;
      const names = w.map((i) => storyLabel(list[i] || {}, i)).join(', ');
      tabs.push(`
        <button type="button" class="wave-tab" role="tab" data-wave="${n + 1}" data-status="${status}"
                aria-selected="${shown === n + 1}" title="${esc(names)}">
          ${tabMark(status)}<span>${esc(label)}</span><span class="count">${delivered}/${w.length}</span>
        </button>`);
    });
    const html = tabs.join('');
    if (el.waveTabs.dataset.html !== html) {
      el.waveTabs.innerHTML = html;
      el.waveTabs.dataset.html = html;
    }
    if (shown !== lastShownWave) {
      lastShownWave = shown;
      el.waveTabs.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest', inline: 'center' });
    }
  }

  // ----- lanes -----
  function runningStep(st) {
    return [...(st.steps || [])].reverse().find((s) => s.status === 'running');
  }

  function laneActivity(st) {
    const running = runningStep(st);
    if (running) {
      const time = running.started_at ? `<span class="time" data-elapsed="${esc(running.started_at)}"></span>` : '';
      return `<span class="spinner" aria-hidden="true"></span><span>${esc(running.title)}</span>${time}`;
    }
    const failedStep = (st.steps || []).find((s) => s.status === 'failed');
    if (failedStep) return `<span>Falhou em ${esc(failedStep.title)}</span>`;
    if (st.status === 'done' || st.status === 'warning') {
      return `<span>Entregue${st.duration_ms ? ` em ${esc(fmtDuration(st.duration_ms))}` : ''}</span>`;
    }
    if (st.status === 'skipped') return '<span>Bloqueada no Passo 0</span>';
    if (st.status === 'canceled') return '<span>Cancelada</span>';
    return `<span>${st.depends_on?.length ? `Depende de ${esc(st.depends_on.join(', '))}` : 'Aguardando a vez'}</span>`;
  }

  function laneHtml(st, i, selected) {
    const steps = (st.steps || []).filter((s) => !isHidden(s));
    const done = steps.filter(finished).length;
    const pct = steps.length ? Math.round((done / steps.length) * 100) : 0;
    const status = STORY_STATUS[st.status] || STORY_STATUS.pending;
    return `
      <button type="button" class="lane" data-story="${i}" data-status="${esc(st.status)}" aria-pressed="${selected}">
        <span class="lane-top"><span class="lane-id">${esc(storyLabel(st, i))}</span>${pill(status)}</span>
        <span class="lane-title">${esc(storyName(st))}</span>
        <span class="lane-now">${laneActivity(st)}</span>
        <span class="lane-bar"><span data-pct="${pct}"></span></span>
        <span class="lane-foot num"><span>${done} de ${steps.length} etapas</span>${st.branch ? `<code title="${esc(st.branch)}">${esc(st.branch)}</code>` : ''}</span>
      </button>`;
  }

  function pickStory(run, wave) {
    const list = run.stories || [];
    if (viewStory !== null && wave.includes(viewStory)) return viewStory;
    return wave.find((i) => list[i]?.status === 'running')
      ?? wave.find((i) => list[i]?.status === 'failed')
      ?? wave[0];
  }

  // ----- step rows -----
  function impactRows(impact) {
    if (!impact) return '';
    const rows = [
      ['Contratos', impact.contracts], ['Consumidores', impact.consumers],
      ['Migrations', impact.migrations], ['Recursos compartilhados', impact.shared_resources],
    ].filter(([, items]) => items?.length)
      .map(([label, items]) => `<dt>${label}</dt><dd>${items.map(esc).join(', ')}</dd>`).join('');
    return rows + (impact.rollback ? `<dt>Reversão</dt><dd>${esc(impact.rollback)}</dd>` : '');
  }

  function renderPlan(plan, steps, key) {
    if (!plan) return '';
    const titles = new Map(steps.map((s) => [s.id, s.title]));
    const agents = plan.agents.map((id) => titles.get(id) || id);
    const streams = plan.streams || [];
    const summary = [agents.join(', '), streams.length ? `${streams.length} frentes` : ''].filter(Boolean).join(' · ');
    const streamList = streams.map((st) => {
      const deps = st.depends_on?.length ? ` <span class="muted">depende de ${st.depends_on.map(esc).join(', ')}</span>` : '';
      return `<li><span class="chip">${esc(titles.get(st.agent) || st.agent)}</span> ${esc(st.title)}${deps}</li>`;
    }).join('');
    const note = PLAN_SOURCE[plan.source];
    return `
      <details class="plan" data-key="${esc(key)}"${openDetails.has(`${key}|details|-1`) ? ' open' : ''}>
        <summary>${icon('chevron')}<b>Plano</b><span>${esc(summary)}</span></summary>
        <div class="plan-body">
          <dl>
            <dt>Agentes</dt><dd><span class="chips">${agents.map((a) => `<span class="chip">${esc(a)}</span>`).join('')}</span></dd>
            ${plan.pattern ? `<dt>Padrão de projeto</dt><dd>${esc(plan.pattern)}</dd>` : ''}
            ${streams.length ? `<dt>Frentes</dt><dd><ul>${streamList}</ul></dd>` : ''}
            ${plan.reason ? `<dt>Motivo</dt><dd>${esc(plan.reason)}</dd>` : ''}
            ${impactRows(plan.impact)}
            ${plan.acceptance?.length ? `<dt>Aceitação</dt><dd><ul>${plan.acceptance.map((a) => `<li>${esc(a.scenario)}${a.method ? ` <span class="muted">(${esc(a.method)}${a.negative ? ', negativo' : ''})</span>` : ''}</li>`).join('')}</ul></dd>` : ''}
            ${plan.non_functional?.length ? `<dt>Não funcionais</dt><dd><ul>${plan.non_functional.map((n) => `<li>${esc(n.kind)}: ${esc(n.requirement)}${n.slo ? ` <span class="muted">(SLO ${esc(n.slo)})</span>` : ' <span class="muted">(sem SLO)</span>'}</li>`).join('')}</ul></dd>` : ''}
            ${plan.gaps?.length ? `<dt>Lacunas</dt><dd><ul>${plan.gaps.map((g) => `<li>${esc(g)}</li>`).join('')}</ul></dd>` : ''}
          </dl>
          ${note ? `<p class="note">${esc(note)}</p>` : ''}
        </div>
      </details>`;
  }

  // Command output quoted in a fenced block is collapsed under the summary.
  function issueDescription(text) {
    const at = text.indexOf('```');
    if (at < 0) return `<span class="issue-desc">${esc(text)}</span>`;
    const code = text.slice(at).replace(/^```[^\n]*\n?/, '').replace(/\n?```\s*$/, '');
    return `<span class="issue-desc">${esc(text.slice(0, at).trim())}</span>
      <details class="check-out"><summary>Ver saída</summary><pre>${esc(code)}</pre></details>`;
  }

  function causeDetail(label, value) {
    return value ? `<dt>${esc(label)}</dt><dd>${esc(value)}</dd>` : '';
  }

  function renderCause(c, index, total) {
    const status = CAUSE_STATUS[c.status] || CAUSE_STATUS.open;
    const attempts = c.attempts ? ` · tentativa ${esc(c.attempts)}` : '';
    const details = [
      causeDetail('Causa raiz', c.root_cause),
      causeDetail('Evidência', c.evidence),
      causeDetail('Estratégia', (c.strategy || []).join('; ')),
      causeDetail('Teste de regressão', c.regression_test),
      causeDetail('Prova', c.proof),
      causeDetail('Motivo', c.reason),
    ].join('');
    return `
      <li>
        <span class="sev" data-sev="${esc(status.sev)}">${esc(status.label)}</span>
        <span class="issue-desc">${esc(c.title || c.fingerprint)}</span>
        <span class="issue-meta"><span>Causa ${index + 1} de ${total}${attempts}</span>${c.owner ? `<span>${esc(c.owner)}</span>` : ''}</span>
        ${details ? `<dl class="cause-details">${details}</dl>` : ''}
      </li>`;
  }

  // Passo 0's readiness: each criterion with its evidence and, when the story
  // waits on the user, the open decisions with a field for each answer.
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
            <td>${esc(a.criterion)}${a.negative ? ' <span class="note">(negativo)</span>' : ''}<br><span class="note">${esc(a.scenario || '')}</span></td>
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
    if (!reports.length && !measures.length) return '';
    const items = reports.flatMap(([id, r]) => [
      ...(r.requirements || []).map((q) => `<tr><td><code>${esc(q.id)}</code> <span class="note">${esc(id)}</span></td><td>${esc(q.text)}</td><td>${esc(q.verification)}</td></tr>`),
      ...(r.problems || []).map((p) => `<tr><td><span class="note">${esc(id)}</span></td><td colspan="2"><span class="note" data-tone="warn">${esc(p)}</span></td></tr>`),
      ...(!r.requirements?.length && r.none_reason ? [`<tr><td><span class="note">${esc(id)}</span></td><td colspan="2">${esc(r.none_reason)}</td></tr>`] : []),
    ]);
    const rows = measures.map((m) => `<tr><td>${esc(m.metric)}</td><td>${esc(m.value)}${m.slo ? ` (meta ${esc(m.slo)})` : ''}</td><td><span class="note" data-tone="${m.met ? 'success' : 'danger'}">${m.met ? 'atingida' : 'não atingida'}</span></td></tr>`);
    return `
      <details class="trace-all">
        <summary>Especialidades: ${reports.length} relatório(s)${measures.length ? `, ${measures.length} medição(ões)` : ''}</summary>
        <table>${items.join('')}${rows.join('')}</table>
      </details>`;
  }

  function renderReadiness(st, si, running) {
    const r = st.readiness;
    if (!r) return '';
    const criteria = (r.criteria || []).map((c) => `
      <li>
        <span class="sev" data-sev="${c.status === 'existente' ? 'baixa' : c.status === 'parcial' ? 'média' : 'alta'}">${esc(CRITERION_LABEL[c.status] || c.status)}</span>
        <span>${esc(c.criterion)}</span>
        <span class="issue-meta">${c.evidence ? `<code>${esc(c.evidence)}</code>` : ''}</span>
      </li>`).join('');
    const answered = (st.answers || []).map((a) => `<li><span></span><span>${esc(a.question)} → <strong>${esc(a.answer)}</strong></span><span></span></li>`).join('');
    const open = r.decisions || [];
    const form = st.status === 'waiting_input' && open.length ? `
      <form class="decisions" data-story="${si}">
        <h3>Decisões de produto em aberto <span class="muted">${open.length}</span></h3>
        ${open.map((d, i) => `
          <label class="field">
            <span>${esc(d.question)}${d.impact ? ` <span class="muted">(${esc(d.impact)})</span>` : ''}</span>
            <textarea name="answer" rows="2" maxlength="2000" required placeholder="${esc((d.options || []).join(' · '))}" data-index="${i}"></textarea>
          </label>`).join('')}
        <button class="btn btn-sm" type="submit"${running ? ' disabled title="Aguarde a execução terminar"' : ''}>Responder e retomar</button>
      </form>` : '';
    return `
      ${criteria ? `<div class="issues"><h3>Critérios no Passo 0 <span class="muted">${(r.criteria || []).length}</span></h3><ul>${criteria}</ul></div>` : ''}
      ${answered ? `<div class="issues"><h3>Decisões respondidas</h3><ul>${answered}</ul></div>` : ''}
      ${form}`;
  }

  el.storyHead.addEventListener('submit', async (e) => {
    const form = e.target.closest('form.decisions');
    if (!form) return;
    e.preventDefault();
    const answers = [...form.querySelectorAll('textarea[name="answer"]')]
      .sort((a, b) => Number(a.dataset.index) - Number(b.dataset.index))
      .map((t) => t.value.trim());
    try {
      await api('POST', '/api/pipeline/answer', { story: Number(form.dataset.story), answers });
      toast('Respostas registradas; a US recomeça do Passo 0.', 'success');
    } catch (err) {
      toast(err.message, 'error');
    }
  });

  function renderCauses(causes) {
    if (!causes?.length) return '';
    return `
      <div class="issues">
        <h3>Causas <span class="muted">${causes.length}</span></h3>
        <ul>${causes.map((c, i) => renderCause(c, i, causes.length)).join('')}</ul>
      </div>`;
  }

  function renderIssues(issues, title) {
    if (!issues?.length) return '';
    return `
      <div class="issues">
        <h3>${esc(title)} <span class="muted">${issues.length}</span></h3>
        <ul>${issues.map((is) => `
          <li>
            ${is.severity ? `<span class="sev" data-sev="${esc(is.severity)}">${esc(is.severity)}</span>` : '<span></span>'}
            <span>${issueDescription(is.description)}</span>
            <span class="issue-meta"><span>${esc(is.agent)}</span>${is.location ? `<code>${esc(is.location)}</code>` : ''}${is.sources?.length ? `<span title="Revisores que apontaram">${esc(is.sources.join(', '))}</span>` : ''}</span>
          </li>`).join('')}
        </ul>
      </div>`;
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
          ${c.report ? `<span class="note" data-tone="${c.report.status === 'lido' ? '' : 'warn'}" title="${esc(c.report.detail || c.report.path || '')}">relatório ${esc(c.report.format)}: ${c.report.status === 'lido' ? `${c.report.passed || 0} ok, ${c.report.failed || 0} falha(s), ${c.report.skipped || 0} ignorado(s)` : esc(c.report.status)}</span>` : ''}
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
        <li data-chosen="${Boolean(c.chosen)}">
          <span class="cand-id">${esc(c.id)}</span>
          <span>${esc(c.strategy)}</span>
          <span class="cand-result">${esc(result)}</span>
        </li>`;
      }).join('')}
      </ul>`;
  }

  function renderCommit(s) {
    const c = s.commit;
    if (!c) return '';
    const files = c.files || [];
    const add = files.reduce((n, f) => n + f.added, 0);
    const del = files.reduce((n, f) => n + f.deleted, 0);
    const names = files.map((f) => f.path).join('\n');
    return `
      <div class="commit">
        ${icon('branch')}<code>${esc(c.hash)}</code>
        <span title="${esc(names)}">${files.length} arquivo${files.length === 1 ? '' : 's'}</span>
        <span class="diffstat"><span data-kind="add">+${add}</span> <span data-kind="del">−${del}</span></span>
        <button class="link" type="button" data-diff="${esc(c.hash)}" data-title="${esc(s.title)}">Ver diff</button>
      </div>`;
  }

  // What each trace status proves; only "comprovado" is a pass.
  const TRACE_TONE = {
    'comprovado': 'success', 'falhou': 'danger', 'ignorado': 'warn', 'sem teste': 'warn',
    'sem relatório': 'warn', 'não executado': 'warn', 'só código de saída': '',
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

  // The dependency analysis, as a table of waves.
  function renderWaves(run) {
    const waves = run.waves || [];
    if (!waves.length) return '';
    const list = run.stories || [];
    const rows = waves.map((w, n) => `
      <tr>
        <td>Onda ${n + 1}</td>
        <td><span class="chips">${w.map((i) => `<span class="chip">${esc(storyLabel(list[i] || {}, i))}</span>`).join('')}</span></td>
        <td class="deps">${esc([...new Set(w.flatMap((i) => list[i]?.depends_on || []))].join(', ') || '—')}</td>
      </tr>`).join('');
    const merged = run.integrations?.length
      ? `<p class="note">Branches de integração: ${run.integrations.map((b) => `<code>${esc(b)}</code>`).join(' ')}</p>` : '';
    return `
      <table class="waves-table">
        <thead><tr><th>Onda</th><th>User Stories</th><th>Depende de</th></tr></thead>
        <tbody>${rows}</tbody>
      </table>${merged}`;
  }

  function stepSide(s, ctx) {
    const parts = [];
    if (s.model) parts.push(`<span class="model" title="${esc([s.provider, s.model, s.effort].filter(Boolean).join(' · '))}">${esc(s.model)}</span>`);
    if (s.status === 'running' && s.started_at) parts.push(`<span class="time" data-elapsed="${esc(s.started_at)}"></span>`);
    else if (s.duration_ms) parts.push(`<span class="time">${esc(fmtDuration(s.duration_ms))}</span>`);
    if (s.status === 'running') parts.push(pill({ label: 'Executando', tone: 'accent' }));
    if (s.status === 'failed') parts.push(pill({ label: 'Falhou', tone: 'danger' }));
    if (s.status === 'warning') parts.push(pill({ label: 'Com pendências', tone: 'warn' }));
    if (s.stoppedHere && s.status === 'canceled') parts.push(pill({ label: 'Parou aqui', tone: 'warn' }));
    if (s.status === 'skipped') parts.push(`<span>${ctx.isRunStep ? 'Reutilizado' : 'Dispensada'}</span>`);
    // A cleanup is not resumable: a new one starts from a fresh baseline.
    if (s.status === 'failed' && !ctx.run.running && ctx.run.kind !== 'cleanup') {
      parts.push(`<button class="btn btn-sm" type="button" data-retry-step="${esc(s.id)}" data-retry-story="${ctx.isRunStep ? -1 : ctx.si}">${icon('rotate')}Refazer</button>`);
    }
    return parts.join('');
  }

  function stepBody(s, key, ctx) {
    const blocks = [];
    if (s.error) blocks.push(`<div class="step-error">${esc(s.error)}</div>`);
    if (s === ctx.run.analysis) blocks.push(renderWaves(ctx.run));
    if (s.kind === 'fix') blocks.push(renderIssues(s.issues, 'Correções solicitadas'));
    else if (s.issues?.length) blocks.push(renderIssues(s.issues, ['failed', 'warning'].includes(s.status) ? 'Problemas não resolvidos' : 'Problemas encontrados'));
    if (s.advisory?.length) blocks.push(renderIssues(s.advisory, 'Recomendações de estilo (não bloqueiam)'));
    blocks.push(renderChecks(s.checks), renderCandidates(s.candidates));
    if (ctx.withTrace) blocks.push(renderTrace(ctx.story?.trace));
    blocks.push(renderCommit(s));
    if (s.output) {
      artifacts.set(key, s.output);
      blocks.push(`
        <div class="output">
          <button class="btn btn-sm" type="button" data-copy="${esc(key)}">${icon('copy')}Copiar</button>
          <pre>${esc(s.output)}</pre>
        </div>`);
    }
    return blocks.filter(Boolean).join('');
  }

  function stepRow(s, ctx) {
    const key = ctx.isRunStep ? s.id : `${ctx.si}:${s.id}`;
    const tag = KIND_TAGS[s.kind] ? `<span class="tag">${KIND_TAGS[s.kind]}</span>` : '';
    const sub = [s.note || s.role, s.round && s.kind !== 'stream' ? `rodada ${s.round}` : ''].filter(Boolean).join(' · ');
    const row = `
      <span class="mark">${stepMarker(s)}</span>
      <span class="step-main">
        <span class="step-title"><span>${esc(s.title)}</span>${tag}</span>
        ${sub ? `<span class="step-sub">${esc(sub)}</span>` : ''}
      </span>
      <span class="step-side">${stepSide(s, ctx)}</span>`;
    const body = stepBody(s, key, ctx);
    if (!body) return `<li class="step" data-status="${esc(s.status)}" data-key="${esc(key)}"><div class="step-row">${row}</div></li>`;
    const detailsKey = `${key}|details|0`;
    const open = openDetails.has(detailsKey) || (s.status === 'failed' && !userClosed.has(detailsKey));
    return `
      <li class="step" data-status="${esc(s.status)}" data-key="${esc(key)}">
        <details${open ? ' open' : ''}><summary class="step-row">${row}</summary><div class="step-body">${body}</div></details>
      </li>`;
  }

  function stepsHtml(steps, ctx) {
    const shown = steps.filter((s) => !isHidden(s.step));
    const hidden = steps.length - shown.length;
    let lastVerifier = -1;
    shown.forEach((x, i) => { if (!x.isRunStep && isVerifier(x.step) && x.step.status !== 'pending') lastVerifier = i; });
    return shown.map((x, i) => stepRow(x.step, { ...ctx, isRunStep: x.isRunStep, withTrace: i === lastVerifier })).join('') +
      (hidden ? `<li class="steps-hidden">${hidden} etapa${hidden === 1 ? '' : 's'} não executada${hidden === 1 ? '' : 's'}: não necessária${hidden === 1 ? '' : 's'} ou cancelada${hidden === 1 ? '' : 's'}.</li>` : '');
  }

  // ----- header -----
  function storyStepTitle(st) {
    return runningStep(st)?.title || '';
  }

  function runFinding(run) {
    const list = run.stories || [];
    if (!run.started_at) return 'Nenhuma execução nesta sessão.';
    const label = (st) => `<strong>${esc(storyLabel(st, list.indexOf(st)))}</strong>`;
    if (run.running) {
      const running = list.filter((s) => s.status === 'running');
      if (running.length) return running.map((st) => `${label(st)} ${esc(storyStepTitle(st) || 'em andamento')}`).join(' · ');
      if (run.discovery?.status === 'running') return 'Fazendo o discovery do repositório.';
      if (run.analysis?.status === 'running') return `Mapeando as dependências entre ${list.length} US.`;
      return 'Preparando a próxima onda.';
    }
    if (run.kind === 'cleanup') return cleanupFinding(run);
    const failedStory = list.find((s) => s.status === 'failed');
    if (run.result === 'failed') {
      const step = failedStory?.steps.find((s) => s.status === 'failed') || [run.discovery, run.analysis].find((s) => s?.status === 'failed');
      const where = failedStory ? `${label(failedStory)} · ` : '';
      return `Parou em ${where}${esc(step?.title || 'uma etapa')}. Continue de onde parou ou abra a etapa para refazer.`;
    }
    if (run.result === 'canceled') {
      const at = run.resume;
      return at ? `Interrompida em ${esc([at.story_title, at.title].filter(Boolean).join(' · '))}. Continue de onde parou.` : 'Execução cancelada.';
    }
    const delivered = list.filter((s) => s.status === 'done' || s.status === 'warning').length;
    const pending = list.filter((s) => s.status === 'warning' || s.status === 'skipped').length;
    const took = run.finished_at ? ` em ${fmtDuration(Date.parse(run.finished_at) - Date.parse(run.started_at))}` : '';
    const out = list.filter((s) => s.integration === 'blocked').length;
    const kept = out ? ` ${out} fora da branch base.` : '';
    const v = VALIDATION[run.validation];
    const validation = v ? ` Validação ${v.short}${run.dry_run ? ' (simulação)' : ''}.` : '';
    return `Concluída${esc(took)}: ${delivered} de ${list.length} US entregue${list.length === 1 ? '' : 's'}${pending ? `, ${pending} com pendências` : ''}.${validation}${kept}`;
  }

  function cleanupFinding(run) {
    const took = run.finished_at ? ` em ${fmtDuration(Date.parse(run.finished_at) - Date.parse(run.started_at))}` : '';
    if (run.result === 'succeeded') return `Limpeza aprovada${esc(took)}. Revise a branch antes de integrar.`;
    if (run.cleanup?.verdict === 'rejected') return 'Limpeza reprovada. Os motivos estão abaixo; a branch ficou para inspeção.';
    if (run.result === 'canceled') return 'Limpeza cancelada. O que já foi feito ficou na branch.';
    return `Limpeza parou. <a href="#saude">Inicie uma nova</a> depois de corrigir a causa.`;
  }

  function stat(label, value, tone = '') {
    return `<div><dt>${esc(label)}</dt><dd${tone ? ` data-tone="${tone}"` : ''}>${value}</dd></div>`;
  }

  function renderStats(run) {
    const list = run.stories || [];
    const elapsed = `<span data-elapsed="${esc(run.started_at)}"${run.finished_at ? ` data-end="${esc(run.finished_at)}"` : ''}></span>`;
    let cells;
    if (list.length > 1) {
      const delivered = list.filter((s) => s.status === 'done' || s.status === 'warning').length;
      const running = list.filter((s) => s.status === 'running').length;
      const pending = list.filter((s) => s.status === 'warning' || s.status === 'skipped').length;
      cells = [
        stat('Entregues', `${delivered} <small>de ${list.length}</small>`),
        stat('Em execução', String(running)),
        stat('Com pendências', String(pending), pending ? 'warn' : ''),
        stat('Tempo', elapsed),
      ];
    } else {
      const steps = (list[0]?.steps || []).filter((s) => !isHidden(s));
      const fixes = steps.filter((s) => s.kind === 'fix').length;
      cells = [
        stat('Etapas', `${steps.filter(finished).length} <small>de ${steps.length}</small>`),
        stat('Correções', String(fixes)),
        stat('Tempo', elapsed),
      ];
    }
    const html = cells.join('');
    if (el.runStats.dataset.html !== html) {
      el.runStats.innerHTML = html;
      el.runStats.dataset.html = html;
    }
  }

  function renderHeader(run) {
    const started = Boolean(run.started_at);
    el.runMeta.innerHTML = runFinding(run);
    el.runStats.hidden = !started;
    el.progress.hidden = !started;
    el.runEmpty.hidden = started;
    el.topRun.hidden = !started;
    el.navLive.hidden = !run.running;

    let state = { label: 'Ocioso', tone: 'neutral' };
    if (run.running) state = { label: 'Em execução', tone: 'accent' };
    else if (run.result) state = RUN_RESULT[run.result];
    if (!run.running && run.result === 'succeeded' && VALIDATION[run.validation] && run.validation !== 'passed') {
      state = { label: `Concluído · ${VALIDATION[run.validation].short}`, tone: 'warn' };
    }
    el.runBadge.dataset.tone = state.tone;
    el.runBadge.textContent = state.label;
    if (!started) return;
    el.topElapsed.innerHTML = `<span data-elapsed="${esc(run.started_at)}"${run.finished_at ? ` data-end="${esc(run.finished_at)}"` : ''}></span>`;
    renderStats(run);

    const list = run.stories || [];
    const runSteps = [run.discovery, run.analysis].filter(Boolean);
    const total = runSteps.length + list.reduce((n, st) => n + st.steps.length, 0);
    const done = runSteps.filter(finished).length + list.reduce((n, st) => n + st.steps.filter(finished).length, 0);
    el.progressBar.style.width = `${total ? (done / total) * 100 : 0}%`;
    el.progress.setAttribute('aria-valuemax', String(total));
    el.progress.setAttribute('aria-valuenow', String(done));
    el.progress.dataset.result = run.result || '';
  }

  // ----- the run -----
  // Flags the step a stopped run resumes from, so it stays visible and marked.
  function markResumeStep(run) {
    const at = run.running ? null : run.resume;
    if (!at) return;
    const steps = at.story < 0 ? [run.discovery, run.analysis] : run.stories?.[at.story]?.steps || [];
    const step = steps.find((s) => s?.id === at.step);
    if (step) step.stoppedHere = true;
  }

  function renderRun(run) {
    markResumeStep(run);
    renderHeader(run);
    renderCleanup(run);
    artifacts = new Map();
    const list = run.stories || [];
    if (!run.started_at || !list.length) {
      el.waveTabs.hidden = el.waveHead.hidden = el.lanes.hidden = el.storyPanel.hidden = true;
      return;
    }
    el.storyPanel.hidden = false;
    const runSteps = [run.discovery, run.analysis].filter(Boolean);

    // One story: no waves to navigate; preparation and story share one list.
    if (list.length === 1) {
      el.waveTabs.hidden = el.waveHead.hidden = el.lanes.hidden = true;
      renderStory(run, 0, runSteps);
      return;
    }

    const waves = runWaves(run);
    const current = currentWave(run, waves);
    const shown = viewWave !== null && viewWave <= waves.length ? viewWave : current;
    el.waveTabs.hidden = false;
    renderWaveTabs(run, waves, shown);

    if (shown === 0) {
      el.waveHead.hidden = el.lanes.hidden = true;
      renderPreparation(run, runSteps);
      return;
    }
    const wave = waves[shown - 1];
    const si = pickStory(run, wave);
    const parallel = wave.length > 1;
    el.waveHead.hidden = !parallel;
    el.waveHead.innerHTML = parallel
      ? `<p>${wave.length} US em paralelo, cada uma na sua branch. São integradas quando todas terminam.</p>` : '';
    el.lanes.hidden = !parallel;
    if (parallel) {
      const html = wave.map((i) => laneHtml(list[i], i, i === si)).join('');
      el.lanes.dataset.count = String(Math.min(wave.length, 4));
      if (el.lanes.dataset.html !== html) {
        el.lanes.innerHTML = html;
        el.lanes.dataset.html = html;
        el.lanes.querySelectorAll('[data-pct]').forEach((n) => { n.style.width = `${n.dataset.pct}%`; });
      }
    }
    renderStory(run, si, []);
  }

  function renderPreparation(run, runSteps) {
    el.storyHead.innerHTML = `
      <h2 id="storyTitle">Preparação</h2>
      <p class="story-meta">Discovery do repositório e análise de dependências entre as ${(run.stories || []).length} US.</p>`;
    paintSteps(stepsHtml(runSteps.map((step) => ({ step, isRunStep: true })), { run, si: -1 }));
  }

  // What proves each kind of change: see the pipeline's change.go.
  const CHANGE_LABEL = { bug: 'Correção de bug', feature: 'Funcionalidade', refactor: 'Refatoração', trivial: 'Edição trivial' };

  function renderStory(run, si, runSteps) {
    const list = run.stories || [];
    const st = list[si];
    const status = STORY_STATUS[st.status] || STORY_STATUS.pending;
    const time = st.status === 'running' && st.started_at
      ? `<span class="num" data-elapsed="${esc(st.started_at)}"></span>`
      : st.duration_ms ? `<span class="num">${esc(fmtDuration(st.duration_ms))}</span>` : '';
    const meta = [
      pill(status),
      VALIDATION[st.validation] ? pill(VALIDATION[st.validation]) : '',
      st.risk && st.risk.level !== 'baixo'
        ? `<span title="${esc((st.risk.reasons || []).join('\n'))}">${pill({ label: `Risco ${st.risk.level.replace('medio', 'médio').replace('critico', 'crítico')}`, tone: st.risk.level === 'medio' ? 'neutral' : 'warn' })}</span>`
        : '',
      INTEGRATION[st.integration] ? `<span title="${esc(st.integration_note || '')}">${pill(INTEGRATION[st.integration])}</span>` : '',
      st.baseline && Object.keys(st.baseline.failing || {}).length
        ? `<span title="${esc(`Já falhavam antes da US: ${Object.keys(st.baseline.failing).join(', ')}`)}">${pill({ label: 'Falhas preexistentes', tone: 'warn' })}</span>`
        : '',
      st.change ? `<span title="${esc(st.change_reason || '')}">${pill({ label: CHANGE_LABEL[st.change] || st.change, tone: 'neutral' })}</span>` : '',
      st.reproduction
        ? `<span title="${esc([st.reproduction.detail, ...(st.reproduction.failures || [])].filter(Boolean).join('\n'))}">${pill({ label: `Bug ${st.reproduction.status}`, tone: st.reproduction.status === 'reproduzida' ? 'success' : 'warn' })}</span>`
        : '',
      st.branch ? `<code>${esc(st.branch)}</code>` : '',
      st.depends_on?.length ? `<span>Depende de ${esc(st.depends_on.join(', '))}</span>` : '',
      time,
    ].filter(Boolean).join('');
    const head = `
      <h2 id="storyTitle">${esc(st.title || 'User Story')}</h2>
      <div class="story-meta">${meta}</div>
      ${renderReadiness(st, si, run.running)}
      ${renderPlan(st.plan, st.steps || [], `plan:${si}`)}
      ${renderSpecialties(st)}
      ${renderAcceptance(st.acceptance)}
      ${renderCauses(st.causes)}`;
    if (el.storyHead.dataset.html !== head) {
      el.storyHead.innerHTML = head;
      el.storyHead.dataset.html = head;
    }
    const steps = [
      ...runSteps.map((step) => ({ step, isRunStep: true })),
      ...(st.steps || []).map((step) => ({ step, isRunStep: false })),
    ];
    paintSteps(stepsHtml(steps, { run, si, story: st }));
  }

  function paintSteps(html) {
    if (html !== lastStepsHtml) {
      lastStepsHtml = html;
      el.steps.innerHTML = html;
      el.steps.querySelectorAll('details').forEach((d) => { if (openDetails.has(panelKey(d, 'details'))) d.open = true; });
      el.steps.querySelectorAll('pre').forEach((p) => {
        const top = scrolled.get(panelKey(p, 'pre'));
        if (top) p.scrollTop = top;
      });
    }
    // The CSP forbids inline style attributes; widths are set through the CSSOM.
    el.storyPanel.querySelectorAll('[data-pct]').forEach((n) => { n.style.width = `${n.dataset.pct}%`; });
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

    const issues = logs.filter((l) => (!logsClearedAt || l.time > logsClearedAt) && (l.level === 'warn' || l.level === 'error')).length;
    el.navIssues.hidden = issues === 0;
    el.navIssues.textContent = String(issues);
    el.navIssues.title = `${issues} aviso${issues === 1 ? '' : 's'} ou erro${issues === 1 ? '' : 's'}`;
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
    let efforts = p.efforts || [];
    for (const id of chosen) {
      const m = p.models.find((x) => x.id === id);
      if (m?.efforts?.length) efforts = efforts.filter((e) => m.efforts.includes(e));
    }
    const effortSel = fs.querySelector('[name="effort"]');
    // A CLI without effort levels (Gemini) has nothing to choose.
    effortSel.closest('.field').hidden = !efforts.length;
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
    el.stopBtn.hidden = !running;
    const resume = running ? null : snapshot?.run.resume;
    el.resumeBtn.hidden = !resume;
    const promotions = snapshot?.run.promotions || [];
    const last = promotions[promotions.length - 1];
    el.reconcileBtn.hidden = running || last?.status !== 'partial';
    el.resumeBtn.title = resume ? `Retoma em ${[resume.story_title, resume.title].filter(Boolean).join(' · ')}` : '';
    el.newRunLink.hidden = running;
    el.startLabel.textContent = running ? 'Execução em andamento…' : 'Iniciar execução';
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
    renderBranches(data.workspace || {});
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

  // ---------- prompts ----------
  let promptItems = [];
  let promptKey = null;
  let nextPromptKey = null;

  const currentPrompt = () => promptItems.find((p) => p.key === promptKey);
  const promptDirty = () => {
    const item = currentPrompt();
    return Boolean(item) && el.promptValue.value.trimEnd() !== item.value.trimEnd();
  };

  function renderPromptList() {
    const groups = [...new Set(promptItems.map((p) => p.group))];
    el.promptList.innerHTML = groups.map((group) => `
      <div class="prompt-group">
        <h2>${esc(group)}</h2>
        ${promptItems.filter((p) => p.group === group).map((p) => `
          <button type="button" data-prompt="${esc(p.key)}"${p.key === promptKey ? ' aria-current="true"' : ''}>
            <span>${esc(p.title)}</span>${p.customized ? '<span class="tag">Customizado</span>' : ''}
          </button>`).join('')}
      </div>`).join('');
  }

  function renderPromptEditor() {
    const item = currentPrompt();
    el.promptForm.hidden = !item;
    if (!item) return;
    el.promptTitle.textContent = item.title;
    el.promptDescription.textContent = item.description;
    el.promptParts.innerHTML = item.parts.map((part) => `<li${part.includes('este texto') ? ' data-current="true"' : ''}>${esc(part)}</li>`).join('');
    el.promptWarning.hidden = !item.warning;
    el.promptWarning.textContent = item.warning || '';
    el.promptValue.value = item.value;
    el.promptDefault.textContent = item.default;
    el.promptContractBox.hidden = !item.contract;
    el.promptContract.textContent = item.contract || '';
    el.promptResetBtn.disabled = !item.customized;
    el.promptDiscard.hidden = true;
    el.promptStatus.textContent = '';
  }

  function selectPrompt(key) {
    promptKey = key;
    nextPromptKey = null;
    renderPromptList();
    renderPromptEditor();
  }

  function applyPrompts(items) {
    promptItems = items;
    if (!promptKey) promptKey = items[0]?.key ?? null;
    renderPromptList();
  }

  function showPromptError(message) {
    el.promptsError.hidden = !message;
    el.promptsError.textContent = message;
  }

  async function loadPrompts() {
    try {
      const keepDraft = promptDirty();
      applyPrompts(await api('GET', '/api/prompts'));
      showPromptError('');
      if (!keepDraft) renderPromptEditor();
    } catch (err) {
      showPromptError(err.message);
    }
  }

  async function savePrompt(call, message) {
    try {
      applyPrompts(await call());
      showPromptError('');
      renderPromptEditor();
      el.promptStatus.textContent = message;
    } catch (err) {
      showPromptError(err.message);
    }
  }

  el.promptList.addEventListener('click', (e) => {
    const key = e.target.closest('[data-prompt]')?.dataset.prompt;
    if (!key || key === promptKey) return;
    if (promptDirty()) {
      nextPromptKey = key;
      el.promptDiscard.hidden = false;
      el.promptDiscardBtn.focus();
      return;
    }
    selectPrompt(key);
  });
  el.promptForm.addEventListener('submit', (e) => {
    e.preventDefault();
    const path = `/api/prompts/${encodeURIComponent(promptKey)}`;
    savePrompt(() => api('PUT', path, { value: el.promptValue.value }), 'Salvo. Vale a partir da próxima execução.');
  });
  el.promptResetBtn.addEventListener('click', () => {
    const path = `/api/prompts/${encodeURIComponent(promptKey)}`;
    savePrompt(() => api('DELETE', path), 'Padrão restaurado.');
  });
  el.promptDiscardBtn.addEventListener('click', () => selectPrompt(nextPromptKey));
  el.promptKeepBtn.addEventListener('click', () => {
    nextPromptKey = null;
    el.promptDiscard.hidden = true;
    el.promptValue.focus();
  });

  // ---------- code health ----------
  const VERDICTS = {
    approved: { label: 'Aprovada', tone: 'success' },
    rejected: { label: 'Reprovada', tone: 'danger' },
  };

  // The result of a cleanup: verdict, branch, reasons, files and the agent's decision log.
  function renderCleanup(run) {
    const c = run.kind === 'cleanup' ? run.cleanup : null;
    el.cleanupCard.hidden = !c;
    if (!c) return;
    const v = VERDICTS[c.verdict] || { label: run.running ? 'Em andamento' : 'Sem veredito', tone: run.running ? 'accent' : 'neutral' };
    const branch = run.stories?.[0]?.branch;
    const list = (items) => `<ul>${items.map((i) => `<li>${esc(i)}</li>`).join('')}</ul>`;
    const html = `
      <div class="cleanup-head">
        <h2 id="cleanupTitle">Saúde do código: ${esc(c.area === '.' ? 'repositório inteiro' : c.area)}</h2>
        <span class="pill" data-tone="${v.tone}">${esc(v.label)}</span>
      </div>
      ${branch ? `<p class="note">Branch <code>${esc(branch)}</code>${c.base_branch ? ` a partir de <code>${esc(c.base_branch)}</code>` : ''}. Nada foi integrado.</p>` : ''}
      ${c.reasons?.length ? `<h3>Motivos</h3>${list(c.reasons)}` : ''}
      ${c.changed?.length ? `<h3>Arquivos alterados (${c.changed.length})</h3>${list(c.changed)}` : ''}
      ${c.decision_log ? `<details class="cleanup-log"><summary>Registro de decisões</summary><pre>${esc(c.decision_log)}</pre></details>` : ''}`;
    if (el.cleanupCard.dataset.html !== html) {
      el.cleanupCard.innerHTML = html;
      el.cleanupCard.dataset.html = html;
    }
  }

  el.healthSuggestBtn.addEventListener('click', async () => {
    el.healthSuggestBtn.disabled = true;
    el.healthAreas.innerHTML = '';
    el.healthWarnings.hidden = true;
    try {
      const s = await api('GET', '/api/health/areas');
      if (!s.areas?.length) {
        el.healthAreas.innerHTML = '<li class="note">Nenhuma área com indicadores. Informe a área à mão.</li>';
      } else {
        el.healthAreas.innerHTML = s.areas.map((a) => `
          <li>
            <button type="button" class="health-area" data-area="${esc(a.path)}">
              <span class="health-area-path">${esc(a.path)}</span>
              <span class="num muted">${a.score.toFixed(2)}</span>
            </button>
            <ul class="health-reasons">${a.reasons.map((r) => `<li>${esc(r)}</li>`).join('')}</ul>
          </li>`).join('');
      }
      if (s.warnings?.length) {
        el.healthWarnings.textContent = s.warnings.join(' ');
        el.healthWarnings.hidden = false;
      }
    } catch (err) {
      toast(err.message, 'error');
    } finally {
      el.healthSuggestBtn.disabled = false;
    }
  });

  el.healthAreas.addEventListener('click', (e) => {
    const pick = e.target.closest('[data-area]');
    if (!pick) return;
    el.healthArea.value = pick.dataset.area;
    el.healthArea.focus();
  });

  el.healthForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    const area = el.healthArea.value.trim();
    if (!area) {
      toast('Informe a área. Use "." para o repositório inteiro.', 'error');
      el.healthArea.focus();
      return;
    }
    const real = el.healthForm.querySelector('input[name="healthMode"]:checked')?.value === 'real';
    el.healthStartBtn.disabled = true;
    try {
      await api('POST', '/api/health/start', {
        area, comments: el.healthComments.checked, tests: el.healthTests.checked,
        rounds: Number(el.healthRounds.value), dry_run: !real,
      });
      openDetails.clear();
      userClosed.clear();
      viewStory = null;
      viewWave = null;
      toast(`Limpeza de ${area === '.' ? 'todo o repositório' : area} iniciada.`, 'success');
      location.hash = '#execucao';
    } catch (err) {
      toast(err.message, 'error');
    } finally {
      el.healthStartBtn.disabled = false;
    }
  });

  // ---------- views ----------
  const VIEWS = {
    execucao: 'Execução', nova: 'Nova execução', repositorio: 'Repositório', provedores: 'Provedores',
    prompts: 'Prompts', saude: 'Saúde do código', logs: 'Logs',
  };
  const viewFromHash = () => (VIEWS[location.hash.slice(1)] ? location.hash.slice(1) : null);

  function showView(name) {
    document.querySelectorAll('.view[data-view]').forEach((v) => { v.hidden = v.dataset.view !== name; });
    document.querySelectorAll('.nav [data-view-link]').forEach((a) => {
      if (a.dataset.viewLink === name) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
    });
    el.crumbView.textContent = VIEWS[name];
    el.sidebar.parentElement.dataset.currentView = name;
    document.title = `${VIEWS[name]} · FazTudo`;
    setMenu(false);
    if (name === 'prompts') loadPrompts();
    if (name === 'logs' && el.autoScroll.checked) el.log.scrollTop = el.log.scrollHeight;
    if (name === 'repositorio') loadRepos();
  }
  function setMenu(open) {
    el.sidebar.toggleAttribute('data-open', open);
    el.menuBtn.setAttribute('aria-expanded', String(open));
  }
  el.menuBtn.addEventListener('click', () => setMenu(!el.sidebar.hasAttribute('data-open')));
  document.addEventListener('click', (e) => {
    if (el.sidebar.hasAttribute('data-open') && !e.target.closest('#sidebar, #menuBtn')) setMenu(false);
  });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') setMenu(false); });
  window.addEventListener('hashchange', () => showView(viewFromHash() || 'execucao'));

  async function boot() {
    updateModeNote();
    await fetchStatus();
    if (!viewFromHash()) history.replaceState(null, '', snapshot?.run.started_at ? '#execucao' : '#nova');
    showView(viewFromHash() || 'execucao');
    if (snapshot?.desktop) {
      setConn('live', 'Aplicativo local');
      startPolling();
      return;
    }
    connect();
  }

  boot();
})();
