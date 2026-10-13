/* 课表 / 时间表 / 科目可视化编辑器
 * 依赖 window.CIMS = { api(path, opts), toast(msg) }（由 index.html 提供）
 */
(function () {
  const $ = s => document.querySelector(s);
  const uid = () => (crypto.randomUUID ? crypto.randomUUID() : String(Date.now()) + Math.random().toString(16).slice(2));

  // 每个 kind 对应一个 Profile 档案（ClassIsland 的档案文件格式）
  const state = {
    subject:   { id: 0, name: '', data: null },
    timelayout:{ id: 0, name: '', data: null },
    classplan: { id: 0, name: '', data: null },
    weekDiv: 0, // 当前编辑的周次类型：0 全周 / 1 单周 / 2 双周
  };

  const EMPTY = () => ({
    Name: '集控档案', TimeLayouts: {}, ClassPlans: {}, Subjects: {},
    IsOverlayClassPlanEnabled: false, OverlayClassPlanId: '', TempClassPlanId: '',
    TempClassPlanSetupTime: '2023-08-20T00:00:00+08:00', IsActive: false,
  });

  /* ---------- 时间工具 ---------- */
  function tzOffset() {
    const off = -new Date().getTimezoneOffset();
    const sign = off >= 0 ? '+' : '-';
    const abs = Math.abs(off);
    const p = n => String(n).padStart(2, '0');
    return sign + p(Math.floor(abs / 60)) + ':' + p(abs % 60);
  }
  // 旧格式：StartSecond = "2023-08-20T07:20:00+08:00"（日期是占位，只取当天时间）
  const toStamp = hhmm => '2023-08-20T' + hhmm + ':00' + tzOffset();
  const fromStamp = s => (typeof s === 'string' && s.length >= 16 ? s.slice(11, 16) : '');
  const pad = n => String(n).padStart(2, '0');

  // 新版本的 ClassIsland 时间点改用 StartTime / EndTime（"07:10:00"），
  // StartSecond / EndSecond 变成空字符串。这里两种格式都认，优先新字段。
  function readTime(tp, isStart) {
    const v = isStart ? tp.StartTime : tp.EndTime;
    if (typeof v === 'string' && v.length >= 5) return v.slice(0, 5);          // "07:10:00" -> "07:10"
    if (typeof v === 'number') {                                                // 少数情况下可能是秒
      return pad(Math.floor(v / 3600)) + ':' + pad(Math.floor((v % 3600) / 60));
    }
    return fromStamp(isStart ? tp.StartSecond : tp.EndSecond);                  // 回退旧字段
  }

  // 写入时同时更新新旧两套字段，保证新旧版本客户端都能正确显示
  function writeTime(tp, startHHMM, endHHMM) {
    tp.StartTime = startHHMM + ':00';
    tp.EndTime = endHHMM + ':00';
    tp.StartSecond = toStamp(startHHMM);
    tp.EndSecond = toStamp(endHHMM);
  }

  /* ---------- 档案读写 ---------- */
  async function loadProfile(kind) {
    const list = await CIMS.api('/api/v1/admin/objects?kind=' + kind);
    if (!list || list.length === 0) { state[kind].data = EMPTY(); state[kind].id = 0; return; }
    const obj = list[0];
    const full = await CIMS.api('/api/v1/admin/objects/' + obj.ID);
    let data = {};
    try { data = JSON.parse(full.Payload || '{}'); } catch (e) { data = EMPTY(); }
    const base = EMPTY();
    state[kind].data = Object.assign(base, data);
    for (const k of ['TimeLayouts', 'ClassPlans', 'Subjects']) {
      if (!state[kind].data[k]) state[kind].data[k] = {};
    }
    state[kind].id = full.ID;
    state[kind].name = full.Name;
  }

  async function saveProfile(kind) {
    const st = state[kind];
    const payload = JSON.stringify(st.data, null, 2);
    if (st.id) {
      await CIMS.api('/api/v1/admin/objects/' + st.id, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ payload }),
      });
    } else {
      const created = await CIMS.api('/api/v1/admin/objects', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ kind, name: defaultName(kind), payload }),
      });
      st.id = created.ID;
      st.name = created.Name;
    }
    CIMS.toast('已保存并发布，客户端下次拉取即更新');
  }

  function defaultName(kind) {
    return { subject: '科目档案', timelayout: '时间表档案', classplan: '课表档案' }[kind] || '档案';
  }

  /* ---------- 科目 ---------- */
  function renderSubjects() {
    const d = state.subject.data;
    const rows = Object.entries(d.Subjects).map(([id, s]) => `
      <tr data-id="${id}">
        <td><input class="s-name" value="${esc(s.Name || '')}" placeholder="语文"></td>
        <td><input class="s-init" value="${esc(s.Initial || '')}" placeholder="语" style="width:60px"></td>
        <td><input class="s-teacher" value="${esc(s.TeacherName || '')}" placeholder="张老师"></td>
        <td><input type="checkbox" class="s-out" ${s.IsOutDoor ? 'checked' : ''}></td>
        <td><button class="danger" data-del-subject="${id}">删除</button></td>
      </tr>`).join('');
    $('#subjectRows').innerHTML = rows || '<tr><td colspan="5" class="muted">还没有科目，点下方「添加科目」</td></tr>';

    document.querySelectorAll('[data-del-subject]').forEach(b => b.onclick = () => {
      delete d.Subjects[b.dataset.delSubject]; renderSubjects();
    });
    document.querySelectorAll('#subjectRows tr[data-id]').forEach(tr => {
      const id = tr.dataset.id, s = d.Subjects[id];
      tr.querySelector('.s-name').onchange = e => s.Name = e.target.value;
      tr.querySelector('.s-init').onchange = e => s.Initial = e.target.value;
      tr.querySelector('.s-teacher').onchange = e => s.TeacherName = e.target.value;
      tr.querySelector('.s-out').onchange = e => s.IsOutDoor = e.target.checked;
    });
  }

  function addSubject() {
    const d = state.subject.data;
    const id = uid();
    d.Subjects[id] = { Name: '新科目', Initial: '', TeacherName: '', IsOutDoor: false, AttachedObjects: {}, IsActive: false };
    renderSubjects();
  }

  /* ---------- 时间表 ---------- */
  function renderTimeLayouts() {
    const d = state.timelayout.data;
    const opts = Object.entries(d.TimeLayouts).map(([id, t]) =>
      `<option value="${id}" ${id === state.currentLayout ? 'selected' : ''}>${esc(t.Name || id.slice(0, 8))}</option>`).join('');
    $('#layoutSelect').innerHTML = opts || '<option value="">（无）</option>';
    if (!state.currentLayout) state.currentLayout = Object.keys(d.TimeLayouts)[0] || '';
    $('#layoutSelect').value = state.currentLayout;
    renderTimePoints();
    refreshClassPlanLayoutOptions();
  }

  function renderTimePoints() {
    const d = state.timelayout.data;
    const tl = d.TimeLayouts[state.currentLayout];
    if (!tl) { $('#tpRows').innerHTML = '<tr><td colspan="6" class="muted">请先在上方新建时间表</td></tr>'; return; }
    $('#tpRows').innerHTML = tl.Layouts.map((tp, i) => `
      <tr data-i="${i}">
        <td>${i + 1}</td>
        <td><input type="time" class="tp-start" value="${readTime(tp, true)}"></td>
        <td><input type="time" class="tp-end" value="${readTime(tp, false)}"></td>
        <td><select class="tp-type">
            <option value="0" ${tp.TimeType === 0 ? 'selected' : ''}>上课</option>
            <option value="1" ${tp.TimeType === 1 ? 'selected' : ''}>课间</option>
            <option value="2" ${tp.TimeType === 2 ? 'selected' : ''}>休息时间</option>
            <option value="3" ${tp.TimeType === 3 ? 'selected' : ''}>隐藏</option>
        </select></td>
        <td><input type="checkbox" class="tp-hide" ${tp.IsHideDefault ? 'checked' : ''}></td>
        <td><button class="danger" data-del-tp="${i}">删除</button></td>
      </tr>`).join('');

    document.querySelectorAll('#tpRows tr[data-i]').forEach(tr => {
      const i = +tr.dataset.i, tp = tl.Layouts[i];
      tr.querySelector('.tp-start').onchange = e => {
        writeTime(tp, e.target.value, readTime(tp, false));
        // 自动衔接上一节的结束时间
        if (i > 0) writeTime(tl.Layouts[i - 1], readTime(tl.Layouts[i - 1], true), e.target.value);
        renderTimePoints();
      };
      tr.querySelector('.tp-end').onchange = e => {
        writeTime(tp, readTime(tp, true), e.target.value);
        // 自动衔接下一节的开始时间
        if (i + 1 < tl.Layouts.length) {
          writeTime(tl.Layouts[i + 1], e.target.value, readTime(tl.Layouts[i + 1], false));
        }
        renderTimePoints();
      };
      tr.querySelector('.tp-type').onchange = e => tp.TimeType = parseInt(e.target.value, 10);
      tr.querySelector('.tp-hide').onchange = e => tp.IsHideDefault = e.target.checked;
    });
    document.querySelectorAll('[data-del-tp]').forEach(b => b.onclick = () => {
      tl.Layouts.splice(+b.dataset.delTp, 1); renderTimePoints();
    });
  }

  function addTimePoint() {
    const d = state.timelayout.data;
    const tl = d.TimeLayouts[state.currentLayout];
    if (!tl) return CIMS.toast('请先新建时间表');
    const prev = tl.Layouts[tl.Layouts.length - 1];
    const start = prev ? readTime(prev, false) : '08:00';
    const [h, m] = start.split(':').map(Number);
    const total = h * 60 + m + 40; // 默认一节 40 分钟
    const end = pad(Math.floor(total / 60) % 24) + ':' + pad(total % 60);
    const tp = { TimeType: 0, IsHideDefault: false, DefaultClassId: '', AttachedObjects: {}, IsActive: false };
    writeTime(tp, start, end);
    tl.Layouts.push(tp);
    renderTimePoints();
  }

  function addTimeLayout() {
    const d = state.timelayout.data;
    const name = prompt('时间表名称（如：周一至周五）', '新时间表');
    if (!name) return;
    const id = uid();
    d.TimeLayouts[id] = { Name: name, Layouts: [], IsActive: false };
    state.currentLayout = id;
    renderTimeLayouts();
  }

  function delTimeLayout() {
    const d = state.timelayout.data;
    if (!state.currentLayout) return;
    if (!confirm('删除当前时间表？使用该时间表的课表需要重新绑定。')) return;
    delete d.TimeLayouts[state.currentLayout];
    state.currentLayout = Object.keys(d.TimeLayouts)[0] || '';
    renderTimeLayouts();
  }

  function renameTimeLayout() {
    const d = state.timelayout.data;
    const tl = d.TimeLayouts[state.currentLayout];
    if (!tl) return;
    const name = prompt('重命名时间表', tl.Name || '');
    if (name) { tl.Name = name; renderTimeLayouts(); }
  }

  /* ---------- 课表网格 ---------- */
  const WEEK = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];

  function refreshClassPlanLayoutOptions() {
    const d = state.timelayout.data;
    const opts = Object.entries(d.TimeLayouts).map(([id, t]) =>
      `<option value="${id}">${esc(t.Name || id.slice(0, 8))}</option>`).join('');
    const sel = $('#cpLayout');
    sel.innerHTML = opts || '<option value="">（请先建时间表）</option>';
    if (!sel.value && state.currentLayout) sel.value = state.currentLayout;
  }

  // 时间表的上课节次增减后，把已排课表的 Classes 长度对齐
  function syncClassPlans(layoutId) {
    const tl = state.timelayout.data.TimeLayouts[layoutId];
    if (!tl) return;
    const n = tl.Layouts.filter(t => t.TimeType === 0).length;
    for (const p of Object.values(state.classplan.data.ClassPlans)) {
      if (p.TimeLayoutId !== layoutId) continue;
      while (p.Classes.length < n) p.Classes.push({ SubjectId: '', IsActive: false });
      if (p.Classes.length > n) p.Classes.length = n;
    }
  }

  function renderGrid() {
    const layoutId = $('#cpLayout').value;
    const tl = state.timelayout.data.TimeLayouts[layoutId];
    const cp = state.classplan.data;
    const subjects = state.subject.data.Subjects;
    if (!tl) { $('#gridBox').innerHTML = '<p class="muted">请先在「时间表」页创建时间点</p>'; return; }
    syncClassPlans(layoutId);

    // 只有 TimeType=0 的时间点是「上课节次」，
    // ClassPlan.Classes 只与这些时间点按顺序一一对应（课间不占 ClassPlan 的位置）。
    let seq = -1;
    const rows = tl.Layouts.map((tp, i) => ({ tp, i }))
      .filter(x => x.tp.TimeType === 0)
      .map(x => { seq += 1; return { tp: x.tp, i: x.i, classIdx: seq }; });
    let html = '<table class="grid"><thead><tr><th style="width:150px">节次</th>';
    [1, 2, 3, 4, 5, 6, 0].forEach(d => html += `<th>${WEEK[d]}</th>`);
    html += '</tr></thead><tbody>';

    const subjOpts = id => Object.entries(subjects).map(([sid, s]) =>
      `<option value="${sid}" ${sid === id ? 'selected' : ''}>${esc(s.Name || '')}</option>`).join('');

    rows.forEach((r, rowIdx) => {
      html += `<tr><td>第${rowIdx + 1}节<br><span class="muted mono">${readTime(r.tp, true)}-${readTime(r.tp, false)}</span></td>`;
      [1, 2, 3, 4, 5, 6, 0].forEach(day => {
        const plan = findClassPlan(cp, layoutId, day, state.weekDiv);
        const subjId = (plan && plan.Classes[r.classIdx] && plan.Classes[r.classIdx].SubjectId) || '';
        html += `<td><select data-day="${day}" data-tp="${r.classIdx}">
          <option value="">—</option>${subjOpts(subjId)}</select></td>`;
      });
      html += '</tr>';
    });
    html += '</tbody></table>';
    $('#gridBox').innerHTML = html;

    document.querySelectorAll('#gridBox select').forEach(sel => {
      sel.onchange = () => {
        const day = +sel.dataset.day, tpIdx = +sel.dataset.tp;
        const plan = ensureClassPlan(cp, layoutId, day, tl);
        plan.Classes[tpIdx].SubjectId = sel.value;
        plan.IsEnabled = plan.Classes.some(c => c.SubjectId);
      };
    });
  }

  // 找到指定时间表 + 星期 + 单双周对应的课表
  // WeekCountDiv: 0 全周 / 1 单周 / 2 双周（周一常会有单周、双周两张课表）
  function findClassPlan(cp, layoutId, weekDay, weekDiv) {
    const div = weekDiv || 0;
    return Object.values(cp.ClassPlans).find(p =>
      p.TimeLayoutId === layoutId && p.TimeRule && p.TimeRule.WeekDay === weekDay &&
      (p.TimeRule.WeekCountDiv || 0) === div) || null;
  }

  // 没有则创建（Classes 长度 = 上课时间点数量）
  function ensureClassPlan(cp, layoutId, weekDay, tl) {
    const div = state.weekDiv || 0;
    let plan = findClassPlan(cp, layoutId, weekDay, div);
    if (plan) return plan;
    const id = uid();
    const classCount = tl.Layouts.filter(t => t.TimeType === 0).length;
    const suffix = div === 1 ? ' 单周' : div === 2 ? ' 双周' : '';
    plan = {
      TimeLayoutId: layoutId,
      TimeRule: { WeekDay: weekDay, WeekCountDiv: div, IsActive: false },
      Classes: Array.from({ length: classCount }, () => ({ SubjectId: '', IsActive: false })),
      Name: WEEK[weekDay] + suffix, IsOverlay: false, OverlaySourceId: null,
      OverlaySetupTime: '2023-08-20T00:00:00+08:00', IsEnabled: false,
      AttachedObjects: {}, IsActive: false,
    };
    cp.ClassPlans[id] = plan;
    return plan;
  }

  function clearEmptyPlans() {
    const cp = state.classplan.data;
    for (const [id, p] of Object.entries(cp.ClassPlans)) {
      if (!p.Classes || !p.Classes.some(c => c.SubjectId)) delete cp.ClassPlans[id];
    }
  }

  /* ---------- 初始化 ---------- */
  async function init() {
    await Promise.all([loadProfile('subject'), loadProfile('timelayout'), loadProfile('classplan')]);
    renderSubjects();
    renderTimeLayouts();
    refreshClassPlanLayoutOptions();
    renderGrid();

    $('#addSubject').onclick = () => { addSubject(); };
    $('#saveSubject').onclick = () => saveProfile('subject');
    $('#addLayout').onclick = addTimeLayout;
    $('#delLayout').onclick = delTimeLayout;
    $('#renameLayout').onclick = renameTimeLayout;
    $('#addTimePoint').onclick = addTimePoint;
    $('#saveLayout').onclick = () => saveProfile('timelayout');
    $('#layoutSelect').onchange = e => { state.currentLayout = e.target.value; renderTimePoints(); };
    $('#cpLayout').onchange = renderGrid;
    $('#cpWeekDiv').onchange = e => { state.weekDiv = parseInt(e.target.value, 10) || 0; renderGrid(); };
    $('#reloadGrid').onclick = async () => {
      await Promise.all([loadProfile('subject'), loadProfile('timelayout'), loadProfile('classplan')]);
      renderTimeLayouts(); refreshClassPlanLayoutOptions(); renderGrid();
      CIMS.toast('已重新载入');
    };
    $('#saveGrid').onclick = async () => {
      clearEmptyPlans();
      await saveProfile('classplan');
      renderGrid();
    };
  }

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, c =>
      ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  }

  window.Planner = { init, state };
})();
