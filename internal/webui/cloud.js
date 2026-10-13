/* 云服务接入：终端用户登录（手机号验证码）+ 云端备份存储
 * 使用 WorkBuddy Cloud SDK 的 CDN 全局形式（本项目是内嵌的单文件 HTML，没有构建步骤）。
 * 初始化参数全部来自 workbuddy_cloud_service 返回的 publicConfig。
 */
(function () {
  const $ = s => document.querySelector(s);

  // publicConfig（由 workbuddy_cloud_service 的 activate 返回，可安全放在前端）
  const PUBLIC_CONFIG = {
    endpoint: 'https://classisland-mgmt.app.workbuddy.host',
    oauthRelayBaseUrl: 'https://www.workbuddy.cn/v2/as/genie-baas/oauth',
    publishableKey: 'wbpk_vtNMl0DtBxRbupecncYL65_Ly94PcaA7Vk7mZpK9T2VUPApstQwPgTm',
  };

  let cloud = null;
  try {
    cloud = WorkBuddyCloud.createWorkBuddyCloud({
      endpoint: PUBLIC_CONFIG.endpoint,
      oauthRelayBaseUrl: PUBLIC_CONFIG.oauthRelayBaseUrl,
      publishableKey: PUBLIC_CONFIG.publishableKey,
    });
  } catch (e) {
    console.error('云服务初始化失败', e.message);
  }
  if (!cloud) {
    // CDN 脚本未加载成功（网络受限）时给出明确提示，而不是让按钮静默失效
    const el = $('#cloudErr');
    if (el) el.textContent = '云服务 SDK 未能加载，请检查能否访问 cdn.jsdelivr.net';
  }

  // 待验证的手机号验证码状态（发送与提交必须分开）
  let pendingOtp = null;

  function showError(msg) {
    const el = $('#cloudErr');
    if (el) el.textContent = msg;
    if (window.toast) window.toast(msg);
  }
  function clearError() {
    const el = $('#cloudErr');
    if (el) el.textContent = '';
  }

  /* ---------- 会话 ---------- */
  async function getSession() {
    if (!cloud) return null;
    const { data, error } = await cloud.auth.getSession();
    if (error) return null;
    return data || null;
  }

  function maskPhone(p) {
    if (!p) return '已登录';
    return p.replace(/(\d{3})\d{4}(\d{4})/, '$1****$2');
  }

  async function refresh() {
    const session = await getSession();
    const logged = !!session;
    $('#cloudLoginBox').classList.toggle('hidden', logged);
    $('#cloudAppBox').classList.toggle('hidden', !logged);
    if (logged) {
      $('#cloudUser').textContent = maskPhone(session.user && (session.user.phone || session.user.email));
      await listBackups();
    }
  }

  /* ---------- 登录（手机号验证码） ---------- */
  $('#sendCodeBtn').onclick = async () => {
    clearError();
    const phone = $('#phoneInput').value.trim();
    if (!/^1\d{10}$/.test(phone)) {
      return showError('请输入正确的 11 位手机号');
    }
    if (!cloud) return showError('云服务未初始化');
    $('#sendCodeBtn').disabled = true;
    const sent = await cloud.auth.sendOtp({ phone });
    $('#sendCodeBtn').disabled = false;
    if (sent.error) return showError(sent.error.message || '验证码发送失败');
    pendingOtp = { phone, verificationId: sent.data.verificationId, isExistingUser: sent.data.isExistingUser };
    $('#codeHint').textContent = '验证码已发送，请查收短信';
    showError('');
  };

  $('#phoneInput').oninput = clearError;

  $('#cloudLoginBtn').onclick = async () => {
    clearError();
    const phone = $('#phoneInput').value.trim();
    const code = $('#codeInput').value.trim();
    if (!pendingOtp || pendingOtp.phone !== phone) {
      return showError('请先为当前手机号获取验证码');
    }
    if (!/^\d{4,8}$/.test(code)) return showError('请输入收到的验证码');
    const done = await cloud.auth.verifyOtp({
      phone: pendingOtp.phone,
      verificationId: pendingOtp.verificationId,
      isExistingUser: pendingOtp.isExistingUser,
      token: code,
    });
    if (done.error) return showError(done.error.message || '验证码校验失败');
    pendingOtp = null;
    $('#codeInput').value = '';
    await refresh();
    if (window.toast) window.toast('已登录云服务');
  };

  $('#cloudLogoutBtn').onclick = async () => {
    await cloud.auth.signOut();
    await refresh();
  };

  /* ---------- 云端备份 ---------- */
  function userPath(session, name) {
    // 路径必须由 SDK 生成，不接受用户输入的原始路径
    return cloud.storage.userPath(session.user.id, 'backups/' + name);
  }

  function tsName(ext) {
    const d = new Date();
    const p = n => String(n).padStart(2, '0');
    return `cims-${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}.${ext}`;
  }

  $('#backupBtn').onclick = async () => {
    const session = await getSession();
    if (!session) return showError('请先登录云服务');
    $('#cloudErr').textContent = '正在导出并上传…';
    try {
      const snap = await window.CIMS.api('/api/v1/admin/export/snapshot');
      const body = new Blob([JSON.stringify(snap)], { type: 'application/json' });
      const path = userPath(session, tsName('json'));
      const up = await cloud.storage.upload(path, body, { contentType: 'application/json', upsert: true });
      if (up.error) return showError('上传失败：' + up.error.message);
      if (window.toast) window.toast('已备份到云端');
      await listBackups();
    } catch (e) {
      showError('备份失败：' + (e && e.message ? e.message : e));
    }
  };

  $('#uploadZipBtn').onclick = async () => {
    const session = await getSession();
    if (!session) return showError('请先登录云服务');
    const f = $('#cloudZipInput').files[0];
    if (!f) return showError('请选择要上传的备份文件');
    try {
      const path = userPath(session, tsName('zip'));
      const up = await cloud.storage.upload(path, f, { contentType: f.type || 'application/zip', upsert: true });
      if (up.error) return showError('上传失败：' + up.error.message);
      if (window.toast) window.toast('文件已存到云端');
      await listBackups();
    } catch (e) {
      showError('上传失败：' + (e && e.message ? e.message : e));
    }
  };

  async function listBackups() {
    const session = await getSession();
    if (!session) return;
    try {
      const res = await cloud.storage.list(`users/${session.user.id}/backups`, {
        limit: 100, offset: 0, sortBy: { column: 'created_at', order: 'desc' },
      });
      if (res.error) return showError('读取云端备份失败：' + res.error.message);
      const items = res.data || [];
      $('#cloudList').innerHTML = items.length
        ? items.map(it => `<tr>
            <td class="mono">${(it.name || '').replace(/^.*\//, '')}</td>
            <td>${it.size ? (it.size / 1024).toFixed(1) + ' KB' : '—'}</td>
            <td>${it.created_at ? new Date(it.created_at).toLocaleString('zh-CN') : '—'}</td>
            <td>
              ${/\.json$/.test(it.name || '') ? `<button class="ghost" data-restore="${it.name}">恢复</button>` : ''}
              <button class="ghost" data-open="${it.name}">下载</button>
              <button class="danger" data-del="${it.name}">删除</button>
            </td></tr>`).join('')
        : '<tr><td colspan="4" class="muted">云端还没有备份</td></tr>';

      document.querySelectorAll('[data-restore]').forEach(b => b.onclick = () => restore(b.dataset.restore));
      document.querySelectorAll('[data-open]').forEach(b => b.onclick = () => openFile(b.dataset.open));
      document.querySelectorAll('[data-del]').forEach(b => b.onclick = () => removeBackup(b.dataset.del));
    } catch (e) {
      showError('读取云端备份失败：' + (e && e.message ? e.message : e));
    }
  }

  async function restore(name) {
    if (!confirm('用这份云端备份覆盖服务器上的现有档案？')) return;
    const session = await getSession();
    if (!session) return showError('请先登录云服务');
    try {
      const blob = await cloud.storage.download(`users/${session.user.id}/backups/${name.split('/').pop()}`);
      const text = typeof blob.text === 'function' ? await blob.text() : await new Response(blob).text();
      const snap = JSON.parse(text);
      const r = await window.CIMS.api('/api/v1/admin/import/snapshot', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(snap),
      });
      if (r && r.ok) {
        if (window.toast) window.toast('已从云端恢复');
        if (window.Planner) window.Planner.init();
      } else {
        showError('恢复失败：' + JSON.stringify(r));
      }
    } catch (e) {
      showError('恢复失败：' + (e && e.message ? e.message : e));
    }
  }

  async function openFile(name) {
    const session = await getSession();
    if (!session) return;
    try {
      const path = `users/${session.user.id}/backups/${name.split('/').pop()}`;
      const signed = await cloud.storage.createSignedUrl(path, 600);
      if (signed.error) return showError(signed.error.message);
      window.open(signed.data.url || signed.data.signedUrl, '_blank');
    } catch (e) {
      showError('获取下载链接失败');
    }
  }

  async function removeBackup(name) {
    if (!confirm('删除这份云端备份？该操作不可恢复。')) return;
    const session = await getSession();
    if (!session) return;
    try {
      const path = `users/${session.user.id}/backups/${name.split('/').pop()}`;
      const r = await cloud.storage.remove([path]);
      if (r.error) return showError(r.error.message);
      await listBackups();
    } catch (e) {
      showError('删除失败');
    }
  }

  window.CloudPanel = { refresh };
})();
