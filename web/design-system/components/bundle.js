/* @ds-bundle: {"format":4,"namespace":"Pail","components":[{"name":"Button"},{"name":"Icon"},{"name":"Status"},{"name":"Field"},{"name":"Command"},{"name":"SourcePicker"},{"name":"DropZone"},{"name":"PailRow"},{"name":"BuildLog"},{"name":"TopBar"}]} */
(function () {
  // React is read when a component renders, so the bundle may load before React does.
  var React = { useState: function (v) { return window.React.useState(v); }, useRef: function (v) { return window.React.useRef(v); }, get Fragment() { return window.React.Fragment; } };
  function h() { var R = window.React; return R.createElement.apply(R, arguments); }
  function cx() { return Array.prototype.filter.call(arguments, Boolean).join(' '); }

  var PATHS = {
    pail: [['path', { d: 'M6.5 9a5.5 5.5 0 0 1 11 0' }], ['path', { d: 'M4 9h16M5.2 9l1.6 10.3a1.5 1.5 0 0 0 1.5 1.2h7.4a1.5 1.5 0 0 0 1.5-1.2L18.8 9M6 13.5h12' }]],
    plus: [['path', { d: 'M12 5v14M5 12h14' }]],
    terminal: [['rect', { x: 3, y: 4.5, width: 18, height: 15, rx: 2 }], ['path', { d: 'm7.5 9.5 3 2.5-3 2.5M13 15h4' }]],
    git: [['circle', { cx: 6, cy: 5.5, r: 2 }], ['circle', { cx: 6, cy: 18.5, r: 2 }], ['circle', { cx: 18, cy: 7.5, r: 2 }], ['path', { d: 'M6 7.5v9M18 9.5v.5a4 4 0 0 1-4 4h-5a3 3 0 0 0-3 3' }]],
    upload: [['path', { d: 'M12 15V4.5M7.5 9 12 4.5 16.5 9M4 15v3a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-3' }]],
    folder: [['path', { d: 'M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z' }]],
    copy: [['rect', { x: 9, y: 9, width: 11, height: 11, rx: 2 }], ['path', { d: 'M15 9V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h3' }]],
    check: [['path', { d: 'm5 12.5 4.5 4.5L19 7.5' }]],
    external: [['path', { d: 'M14 4h6v6M20 4l-9 9M18 14v4a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4' }]],
    globe: [['circle', { cx: 12, cy: 12, r: 9 }], ['path', { d: 'M3 12h18M12 3c2.4 2.6 3.6 5.6 3.6 9s-1.2 6.4-3.6 9c-2.4-2.6-3.6-5.6-3.6-9S9.6 5.6 12 3z' }]],
    redeploy: [['path', { d: 'M20 12a8 8 0 1 1-2.4-5.7M20 4.5v5h-5' }]],
    trash: [['path', { d: 'M4 7h16M9.5 7V4.5h5V7M6.5 7l.9 12.5h9.2L17.5 7' }]]
  };

  function Icon(p) {
    var parts = PATHS[p.name] || PATHS.pail;
    var size = p.size || 18;
    return h('svg', {
      className: cx('pa-icon', p.className), width: size, height: size, viewBox: '0 0 24 24',
      fill: 'none', stroke: 'currentColor', strokeWidth: 1.75, strokeLinecap: 'round', strokeLinejoin: 'round',
      'aria-hidden': p.label ? undefined : true, role: p.label ? 'img' : undefined, 'aria-label': p.label
    }, parts.map(function (x, i) { return h(x[0], Object.assign({ key: i }, x[1])); }));
  }

  function Button(p) {
    var variant = p.variant || 'quiet';
    var rest = Object.assign({}, p);
    delete rest.variant; delete rest.icon; delete rest.size; delete rest.className; delete rest.children;
    return h('button', Object.assign({ type: 'button' }, rest, {
      className: cx('pa-btn', 'pa-btn-' + variant, p.size === 'sm' && 'pa-btn-sm', !p.children && 'pa-btn-icononly', p.className)
    }), p.icon ? h(Icon, { name: p.icon, size: p.size === 'sm' ? 16 : 18 }) : null, p.children);
  }

  var STATUS_WORD = { live: 'Live', building: 'Building', failed: 'Failed', off: 'Off' };
  function Status(p) {
    var s = p.state || 'off';
    return h('span', { className: cx('pa-status', 'pa-status-' + s) },
      h('span', { className: 'pa-dot', 'aria-hidden': true }), p.children || STATUS_WORD[s]);
  }

  var fieldSeq = 0;
  function Field(p) {
    var idRef = React.useRef(null);
    if (!idRef.current) idRef.current = p.id || 'pa-field-' + (++fieldSeq);
    var id = idRef.current;
    var rest = Object.assign({}, p);
    ['label', 'hint', 'error', 'prefix', 'suffix', 'mono', 'className'].forEach(function (k) { delete rest[k]; });
    var describe = p.error ? id + '-err' : (p.hint ? id + '-hint' : undefined);
    return h('div', { className: cx('pa-field', p.error && 'pa-field-error', p.className) },
      p.label ? h('label', { className: 'pa-field-label', htmlFor: id }, p.label) : null,
      h('div', { className: 'pa-field-box' },
        p.prefix ? h('span', { className: 'pa-field-affix' }, p.prefix) : null,
        h('input', Object.assign({ id: id, className: cx('pa-field-input', p.mono && 'pa-mono'), 'aria-invalid': p.error ? true : undefined, 'aria-describedby': describe }, rest)),
        p.suffix ? h('span', { className: 'pa-field-affix' }, p.suffix) : null),
      p.error ? h('p', { id: id + '-err', className: 'pa-field-msg pa-field-msg-error' }, p.error)
        : p.hint ? h('p', { id: id + '-hint', className: 'pa-field-msg' }, p.hint) : null);
  }

  function Command(p) {
    var st = React.useState(false), copied = st[0], setCopied = st[1];
    var text = typeof p.children === 'string' ? p.children : (p.text || '');
    function copy() {
      try { navigator.clipboard && navigator.clipboard.writeText(text); } catch (e) {}
      setCopied(true); setTimeout(function () { setCopied(false); }, 1600);
    }
    return h('div', { className: cx('pa-cmd', p.className) },
      p.comment ? h('div', { className: 'pa-cmd-comment' }, '# ' + p.comment) : null,
      h('div', { className: 'pa-cmd-line' },
        h('code', { className: 'pa-cmd-text' }, h('span', { className: 'pa-cmd-prompt', 'aria-hidden': true }, '$ '), text),
        h('button', { type: 'button', className: 'pa-cmd-copy', onClick: copy, 'aria-label': copied ? 'Copied' : 'Copy command' },
          h(Icon, { name: copied ? 'check' : 'copy', size: 16 }), h('span', null, copied ? 'Copied' : 'Copy'))));
  }

  var DEFAULT_SOURCES = [
    { id: 'cli', label: 'pail-cli', icon: 'terminal', hint: 'From your terminal or CI' },
    { id: 'upload', label: 'Upload', icon: 'upload', hint: 'A folder or a .zip' },
    { id: 'github', label: 'GitHub', icon: 'git' },
    { id: 'gitlab', label: 'GitLab', icon: 'git' },
    { id: 'bitbucket', label: 'Bitbucket', icon: 'git' },
    { id: 'gitea', label: 'Gitea', icon: 'git' },
    { id: 'forgejo', label: 'Forgejo', icon: 'git' }
  ];
  function SourcePicker(p) {
    var sources = p.sources || DEFAULT_SOURCES;
    var st = React.useState(p.defaultValue || null), inner = st[0], setInner = st[1];
    var value = p.value !== undefined ? p.value : inner;
    function pick(id) { setInner(id); p.onChange && p.onChange(id); }
    return h('div', { className: 'pa-sources', role: 'radiogroup', 'aria-label': p.label || 'Where is your site?' },
      sources.map(function (s) {
        var on = s.id === value;
        return h('button', { key: s.id, type: 'button', role: 'radio', 'aria-checked': on, className: cx('pa-source', on && 'is-on'), onClick: function () { pick(s.id); } },
          h(Icon, { name: s.icon || 'git', size: 20 }),
          h('span', { className: 'pa-source-label' }, s.label),
          s.hint ? h('span', { className: 'pa-source-hint' }, s.hint) : null);
      }));
  }

  function DropZone(p) {
    var st = React.useState(false), over = st[0], setOver = st[1];
    var inputRef = React.useRef(null);
    var progress = p.progress;
    function files(list) { if (p.onFiles) p.onFiles(Array.prototype.slice.call(list || [])); }
    var busy = typeof progress === 'number';
    return h('div', {
      className: cx('pa-drop', (over || p.active) && 'is-over', busy && 'is-busy'),
      onDragOver: function (e) { e.preventDefault(); setOver(true); },
      onDragLeave: function () { setOver(false); },
      onDrop: function (e) { e.preventDefault(); setOver(false); files(e.dataTransfer && e.dataTransfer.files); }
    },
      h(Icon, { name: busy ? 'upload' : 'folder', size: 28 }),
      busy
        ? h('div', { className: 'pa-drop-busy' },
            h('p', { className: 'pa-drop-title' }, (p.fileName || 'Uploading') + ' — ' + Math.round(progress * 100) + '%'),
            h('div', { className: 'pa-progress', role: 'progressbar', 'aria-valuemin': 0, 'aria-valuemax': 100, 'aria-valuenow': Math.round(progress * 100) }, h('span', { style: { width: Math.round(progress * 100) + '%' } })))
        : h(React.Fragment, null,
            h('p', { className: 'pa-drop-title' }, (over || p.active) ? 'Let go to drop it in the pail' : 'Drop a folder or a .zip'),
            h('p', { className: 'pa-drop-hint' }, p.hint || 'Static files with an index.html at the top.'),
            h(Button, { size: 'sm', onClick: function () { inputRef.current && inputRef.current.click(); } }, 'Choose files'),
            h('input', { ref: inputRef, type: 'file', multiple: true, hidden: true, onChange: function (e) { files(e.target.files); } })));
  }

  var SOURCE_LABEL = { cli: 'pail-cli', upload: 'Upload', github: 'GitHub', gitlab: 'GitLab', bitbucket: 'Bitbucket', gitea: 'Gitea', forgejo: 'Forgejo' };
  function PailRow(p) {
    return h('div', { className: 'pa-row' },
      h('div', { className: 'pa-row-main' },
        p.onOpen ? h('button', { type: 'button', className: 'pa-row-name pa-row-open', onClick: p.onOpen }, p.name) : h('div', { className: 'pa-row-name' }, p.name),
        h('a', { className: 'pa-row-url', href: p.href || ('http://' + p.url), target: '_blank', rel: 'noreferrer' }, p.url, h(Icon, { name: 'external', size: 14 }))),
      h('div', { className: 'pa-row-meta' },
        h(Status, { state: p.status }),
        h('span', { className: 'pa-row-when' }, [SOURCE_LABEL[p.source] || p.source, p.revision, p.updated].filter(Boolean).join(' · '))),
      p.actions !== false ? h('div', { className: 'pa-row-actions' },
        h(Button, { size: 'sm', variant: 'ghost', icon: 'redeploy', 'aria-label': 'Redeploy ' + p.name, title: 'Redeploy', onClick: p.onRedeploy }),
        h(Button, { size: 'sm', variant: 'ghost', icon: 'trash', 'aria-label': 'Remove ' + p.name, title: 'Remove', onClick: p.onRemove })) : null);
  }

  function BuildLog(p) {
    return h('div', { className: 'pa-log', role: 'log', 'aria-live': 'polite' },
      (p.lines || []).map(function (l, i) {
        var line = typeof l === 'string' ? { text: l } : l;
        return h('div', { key: i, className: cx('pa-log-line', line.level && 'pa-log-' + line.level) },
          line.time ? h('span', { className: 'pa-log-time' }, line.time) : null,
          h('span', null, line.text));
      }));
  }

  function TopBar(p) {
    return h('header', { className: 'pa-top' },
      h('div', { className: 'pa-top-brand' },
        h('svg', { className: 'pa-mark', width: 28, height: 28, viewBox: '0 0 32 32', 'aria-hidden': true },
          h('path', { className: 'pa-mark-fill', d: 'M7.4 17.5h17.2l-1.35 8.3H8.75z' }),
          h('g', { fill: 'none', stroke: 'currentColor', strokeWidth: 2.75, strokeLinecap: 'round', strokeLinejoin: 'round' },
            h('path', { d: 'M8.5 12.5a7.5 7.5 0 0 1 15 0' }), h('path', { d: 'M4.5 12.5h23M6.3 12.5l2.1 13.4a1.6 1.6 0 0 0 1.6 1.4h12a1.6 1.6 0 0 0 1.6-1.4l2.1-13.4' }))),
        h('span', { className: 'pa-top-word' }, 'pail'),
        p.host ? h('span', { className: 'pa-top-host' }, p.host) : null),
      h('div', { className: 'pa-top-actions' }, p.children !== undefined ? p.children : h(Button, { variant: 'primary', icon: 'plus', onClick: p.onNew }, 'New pail')));
  }

  window.Pail = Object.assign(window.Pail || {}, {
    Button: Button, Icon: Icon, Status: Status, Field: Field, Command: Command, SourcePicker: SourcePicker,
    DropZone: DropZone, PailRow: PailRow, BuildLog: BuildLog, TopBar: TopBar
  });
})();
