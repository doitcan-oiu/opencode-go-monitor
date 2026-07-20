// 监控页 Vue 应用。
const { createApp } = Vue;

createApp({
  data() {
    return {
      accounts: [], form: emptyForm(), bulkText: '',
      mode: 'single', showAdd: false, busy: false, refreshing: false,
      msg: '', lastSync: null,
      settings: { warnPercent: 80, expirySoonDays: 3 },
    };
  },
  computed: {
    sortedAccounts() {
      const rank = a => a.status === 'error' ? 0 : this.health(a).level;
      return [...this.accounts].sort((x, y) => rank(x) - rank(y));
    },
    stats() {
      const a = this.accounts;
      const ok = a.filter(x => x.status === 'ok').length;
      const soon = a.filter(x => this.soon(x)).length;
      const bad = a.filter(x => x.status === 'error' || this.tight(x)).length;
      return [
        { label: '总账号', value: a.length, cls: 'text-ink-strong' },
        { label: '正常', value: ok, cls: 'text-primary' },
        { label: '即将到期', value: soon, cls: soon ? 'text-warn' : 'text-ink-strong' },
        { label: '异常/紧张', value: bad, cls: bad ? 'text-danger' : 'text-ink-strong' },
      ];
    },
    aggRows() {
      return [
        { label: '滚动', pct: this.avg('rolling') },
        { label: '每周', pct: this.avg('weekly') },
        { label: '每月', pct: this.avg('monthly') },
      ];
    },
  },
  methods: {
    fmtDur: OC.fmtDur, fmtDate: OC.fmtDate, fmtClock: OC.fmtClock,
    async load() {
      this.accounts = await OC.api('/api/accounts');
      this.lastSync = new Date().toISOString();
    },
    async loadSettings() {
      try { this.settings = await OC.api('/api/settings'); } catch (e) {}
    },
    openAdd() { this.msg = ''; this.showAdd = true; },
    closeAdd() { this.showAdd = false; },
    async addSingle() {
      if (!this.form.workspaceID && !this.form.account.includes('----')) { this.flash('请填写工作空间 ID'); return; }
      this.busy = true;
      try {
        await OC.api('/api/accounts', OC.json('POST', this.form));
        this.form = emptyForm(); this.showAdd = false; this.flash('已添加，正在后台抓取…');
        await this.wait(1200); this.load();
      } catch (e) { this.flash(e.message); } finally { this.busy = false; }
    },
    async addBulk() {
      this.busy = true;
      try {
        const d = await OC.api('/api/accounts/bulk', OC.json('POST', { text: this.bulkText }));
        this.bulkText = ''; this.showAdd = false; this.flash('已导入 ' + d.added + ' 个账号，正在后台抓取…');
        await this.wait(1500); this.load();
      } catch (e) { this.flash(e.message); } finally { this.busy = false; }
    },
    async refreshAll() {
      this.refreshing = true;
      try { await OC.api('/api/refresh', { method: 'POST' }); } catch (e) {}
      this.refreshing = false; this.load();
    },
    async refreshOne(a) {
      if (a._busy) return;
      a._busy = true; this.flash('正在刷新 ' + (a.account || a.workspaceID) + ' …');
      try { Object.assign(a, await OC.api('/api/accounts/' + a.id + '/refresh', { method: 'POST' })); this.flash('已刷新'); }
      catch (e) { this.flash('刷新失败：' + e.message); } finally { a._busy = false; }
    },
    async editField(a, field, label) {
      const v = prompt('输入新的 ' + label + '：', '');
      if (v === null) return;
      try { await OC.api('/api/accounts/' + a.id, OC.json('PUT', { [field]: v })); await this.wait(800); this.load(); this.flash('已更新'); }
      catch (e) { this.flash('更新失败：' + e.message); }
    },
    async del(a) {
      if (!confirm('删除账号 ' + (a.account || a.workspaceID) + ' ?')) return;
      try { await OC.api('/api/accounts/' + a.id, { method: 'DELETE' }); this.load(); } catch (e) { this.flash(e.message); }
    },
    usageRows(a) {
      return [
        { key: 'r', label: '滚动用量', data: a.rolling },
        { key: 'w', label: '每周用量', data: a.weekly },
        { key: 'm', label: '每月用量', data: a.monthly },
      ];
    },
    avg(key) {
      const v = this.accounts.filter(a => a.status === 'ok' && a[key]).map(a => a[key].usagePercent);
      return v.length ? Math.round(v.reduce((s, x) => s + x, 0) / v.length) : null;
    },
    tight(a) { return a.status === 'ok' && [a.rolling, a.weekly, a.monthly].some(u => u && u.usagePercent >= this.settings.warnPercent); },
    soon(a) { return a.expiresAt && a.expiresIn > 0 && a.expiresIn < this.settings.expirySoonDays * 86400; },
    health(a) {
      if (a.status === 'pending') return { label: '抓取中', cls: 'bg-canvas-soft text-mute', level: 5 };
      if (a.status === 'error') return { label: '异常', cls: 'bg-danger/15 text-danger', level: 0 };
      if (a.expiresAt && a.expiresIn <= 0) return { label: '已到期', cls: 'bg-danger/15 text-danger', level: 1 };
      if ([a.rolling, a.weekly, a.monthly].some(u => u && u.usagePercent >= 100)) return { label: '额度用尽', cls: 'bg-danger/15 text-danger', level: 1 };
      if (this.tight(a)) return { label: '额度紧张', cls: 'bg-warn/15 text-warn', level: 2 };
      if (this.soon(a)) return { label: '即将到期', cls: 'bg-warn/15 text-warn', level: 2 };
      return { label: '正常', cls: 'bg-primary/15 text-primary', level: 4 };
    },
    barCls(u) { return this.barClsN(u ? u.usagePercent : null); },
    pctText(u) { return this.pctTextN(u ? u.usagePercent : null); },
    barClsN(p) { if (p == null) return 'bg-hairline'; if (p >= 100) return 'bg-danger'; if (p >= this.settings.warnPercent) return 'bg-warn'; return 'bg-primary'; },
    pctTextN(p) { if (p == null) return 'text-mute'; if (p >= 100) return 'text-danger'; if (p >= this.settings.warnPercent) return 'text-warn'; return 'text-body'; },
    expiryCls(a) { if (!a.expiresAt) return 'text-mute'; if (a.expiresIn <= 0) return 'text-danger'; if (this.soon(a)) return 'text-warn'; return 'text-ink'; },
    tabCls(on) { return on ? 'bg-primary text-canvas' : 'text-body hover:text-ink'; },
    flash(m) { this.msg = m; clearTimeout(this._t); this._t = setTimeout(() => this.msg = '', 4000); },
    wait(ms) { return new Promise(r => setTimeout(r, ms)); },
  },
  mounted() {
    this.loadSettings();
    this.load();
    setInterval(() => this.load(), 30000);
  },
}).mount('#app');

function emptyForm() { return { account: '', password: '', auxEmail: '', workspaceID: '', auth: '', apiKey: '' }; }
