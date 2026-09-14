package ui

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>tenzing agent</title>
<style>
  :root {
    --bg: #1a1a2e;
    --bg2: #16213e;
    --fg: #e0e0e0;
    --fg-dim: #888;
    --accent: #0f3460;
    --blue: #53a8ff;
    --yellow: #e2b93d;
    --red: #e74c3c;
    --green: #2ecc71;
    --mono: 'SF Mono', 'Fira Code', 'Cascadia Code', monospace;
    /* Width of the centered column: 120 characters of transcript text
       (63.25rem at the 14px body font — the mono stack's advance is 0.6em,
       so 120 chars is ~1011px) plus the 1rem of horizontal padding each
       strip carries. Not in ch: that unit resolves against each element's
       own font size, which would leave the 12px status bar narrower than
       the transcript. Retune if the body font size changes. */
    --col: 65.25rem;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    font-family: var(--mono);
    font-size: 0.875rem; /* 14px — user and assistant messages */
    background: var(--bg);
    color: var(--fg);
    height: 100vh;
    display: flex;
    flex-direction: column;
  }
  /* One centered column: transcript, status bar and composer share --col
     so their left edges line up. */
  body > * { width: 100%; max-width: var(--col); margin-inline: auto; }
  #chat {
    flex: 1;
    overflow-y: auto;
    padding: 1rem;
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .msg { max-width: 100%; white-space: pre-wrap; word-break: break-word; line-height: 1.5; }
  .msg.user { color: var(--blue); }
  .msg.user::before { content: '❯ '; }
  .msg.assistant { color: var(--fg); }
  .msg.thinking { color: var(--fg-dim); font-style: italic; font-size: 0.75rem; }
  .msg.thinking::before { content: '✻ '; }
  /* 3 rows at the inherited line-height of 1.5. overflow is hidden and the
     box is scrolled to the bottom on each delta, so the newest rows show. */
  .msg.thinking .preview { max-height: 4.5em; overflow: hidden; padding-left: 1rem; }
  .msg.tool { color: var(--yellow); font-size: 0.75rem; opacity: 0.8; }
  .msg.tool.sub { padding-left: 1rem; opacity: 0.65; }
  .msg.tool.result { color: var(--fg-dim); padding-left: 1rem; }
  .msg.subagent { color: var(--green); font-size: 0.75rem; }
  .msg.tool-progress { color: var(--fg-dim); font-size: 0.75rem; padding-left: 1rem; }
  .msg.error { color: var(--red); font-size: 0.75rem; }
  .msg.error::before { content: '✗ '; }
  .msg.approval { color: var(--yellow); font-size: 0.75rem; border: 1px solid var(--yellow); border-radius: 4px; padding: 0.5rem; }
  .msg.approval button { margin-right: 0.5rem; margin-top: 0.5rem; }
  .msg.approval .deny { background: var(--red); }
  .msg.approval input.pattern { margin-top: 0.5rem; font-family: inherit; font-size: inherit; padding: 0.15rem 0.3rem; }
  .msg.approval pre.reason { margin: 0.4rem 0 0; opacity: 0.85; white-space: pre-wrap; }
  .diff { margin-top: 0.25rem; overflow-x: auto; white-space: pre; font-size: 0.75rem; line-height: 1.35; }
  .diff .add { color: var(--green); }
  .diff .del { color: var(--red); }
  .diff .hunk { color: var(--blue); }
  .diff .meta { opacity: 0.55; }
  .expand { cursor: pointer; text-decoration: underline dotted; }
  .msg.system { color: var(--fg-dim); font-size: 0.75rem; }
  .msg.streaming { color: var(--fg); }
  .msg.streaming::after { content: '▊'; animation: blink 1s step-end infinite; }
  @keyframes blink { 50% { opacity: 0; } }

  #input-area {
    border-top: 1px solid var(--accent);
    padding: 0.75rem 1rem;
    display: flex;
    align-items: flex-end;
    gap: 0.5rem;
    background: var(--bg2);
  }
  #status { font-size: 0.75rem; color: var(--fg-dim); padding: 0.25rem 1rem; background: var(--bg2); }
  #status .ctx { color: var(--green); }
  #status .ctx.warn { color: var(--yellow); }
  #status .ctx.crit { color: var(--red); }
  #query {
    flex: 1;
    background: transparent;
    border: 1px solid var(--accent);
    border-radius: 4px;
    color: var(--fg);
    font-family: var(--mono);
    font-size: 1rem; /* 16px — the composer reads larger than the UI chrome */
    line-height: 1.4;
    padding: 0.5rem;
    resize: none;
    overflow-y: auto;
    outline: none;
  }
  #query:focus { border-color: var(--blue); }
  button {
    background: var(--accent);
    color: var(--fg);
    border: none;
    border-radius: 4px;
    padding: 0.5rem 1rem;
    font-family: var(--mono);
    cursor: pointer;
    font-size: 0.75rem;
  }
  button:hover { background: var(--blue); }
  button:disabled { opacity: 0.4; cursor: default; }
  #cancel-btn { background: var(--red); display: none; }
  #cancel-btn:hover { opacity: 0.8; }
  #cmdmenu {
    display: none;
    flex-direction: column;
    border-top: 1px solid var(--accent);
    background: var(--bg2);
    max-height: 11rem;
    overflow-y: auto;
  }
  #cmdmenu.open { display: flex; }
  #cmdmenu .item { display: flex; gap: 0.75rem; padding: 0.3rem 1rem; cursor: pointer; font-size: 0.75rem; }
  #cmdmenu .item .name { color: var(--blue); min-width: 9rem; }
  #cmdmenu .item .desc { color: var(--fg-dim); }
  #cmdmenu .item.sel { background: var(--accent); }
  #cmdmenu .item.sel .desc { color: var(--fg); }
  #attachments { display: none; gap: 0.5rem; padding: 0.5rem 1rem 0; background: var(--bg2); flex-wrap: wrap; }
  #attachments.has-items { display: flex; }
  .chip { position: relative; }
  .chip img { height: 48px; border: 1px solid var(--accent); border-radius: 4px; display: block; }
  .chip button {
    position: absolute; top: -6px; right: -6px;
    padding: 0; width: 16px; height: 16px; line-height: 14px;
    border-radius: 50%; background: var(--red); font-size: 0.7rem;
  }
  pre { background: #0d1117; padding: 0.5rem; border-radius: 4px; overflow-x: auto; margin: 0.25rem 0; }
  code { font-family: var(--mono); }
  .msg h1, .msg h2, .msg h3 { font-size: 1em; font-weight: bold; margin: 0.4rem 0 0.2rem; }
  .msg ul, .msg ol { margin: 0.2rem 0 0.2rem 1.2rem; }
  .msg code { background: #0d1117; padding: 0 0.2rem; border-radius: 3px; }
  .msg pre code { background: none; padding: 0; }
  .msg a { color: var(--blue); }
</style>
</head>
<body>

<div id="chat"></div>
<div id="status"></div>
<div id="cmdmenu"></div>
<div id="attachments"></div>
<div id="input-area">
  <textarea id="query" rows="2" placeholder="ask something..." autofocus></textarea>
  <button id="send-btn" onclick="send()">send</button>
  <button id="cancel-btn" onclick="cancel()">cancel</button>
</div>

<script>
// DEBUG mirrors the server's --debug flag (substituted by handleIndex).
// Off: thinking and tool output collapse to one-line status entries.
const DEBUG = __DEBUG__;

function esc(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

// ponytail: enough Markdown for model answers — fenced code, inline code,
// bold, italic, headings, lists, http(s) links. The source is escaped
// before a single tag is added and code fences are stashed out of the way
// first, so no model-authored HTML or javascript: URI can reach the DOM.
// Swap in marked.js + DOMPurify if tables or nested lists ever matter.
function renderMarkdown(src) {
  const fences = [];
  let out = esc(src).replace(/\u0060\u0060\u0060[\w-]*\n?([\s\S]*?)\u0060\u0060\u0060/g, (_, code) =>
    '\u0000' + (fences.push('<pre><code>' + code.replace(/\n$/, '') + '</code></pre>') - 1) + '\u0000');

  out = out
    .replace(/^### (.*)$/gm, '<h3>$1</h3>')
    .replace(/^## (.*)$/gm, '<h2>$1</h2>')
    .replace(/^# (.*)$/gm, '<h1>$1</h1>')
    .replace(/^(?:\d+\. .*(?:\n|$))+/gm, m => '<ol>' + m.replace(/^\d+\. (.*)$/gm, '<li>$1</li>') + '</ol>')
    .replace(/^(?:[-*] .*(?:\n|$))+/gm, m => '<ul>' + m.replace(/^[-*] (.*)$/gm, '<li>$1</li>') + '</ul>')
    .replace(/\u0060([^\u0060\n]+)\u0060/g, '<code>$1</code>')
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/\*([^*\n]+)\*/g, '<em>$1</em>')
    .replace(/\[([^\]]+)\]\((https?:[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');

  // The .msg white-space is pre-wrap, so the newlines framing the block
  // tags above would render as blank lines on top of the tags' own margins.
  const block = '(?:ul|ol|li|h[1-3]|pre)';
  out = out
    .replace(new RegExp('\\n(?=<' + block + '\\b)', 'g'), '')
    .replace(new RegExp('(</' + block + '>)\\n', 'g'), '$1');

  return out.replace(/\u0000(\d+)\u0000/g, (_, i) => fences[i]);
}

// renderInto swaps an element's accumulated Markdown source for its
// rendered form. Streaming stays plain text — one parse at the end beats
// re-parsing on every delta.
function renderInto(el) {
  if (el) el.innerHTML = renderMarkdown(el.textContent);
}

// PRIMARY_ARG names the argument that identifies a call, per builtin tool.
// Tools outside this table (extension, subagent and MCP tools) fall back to
// whichever key the model sent first.
const PRIMARY_ARG = {
  bash: 'command',
  ls: 'path',
  Read: 'file_path',
  Write: 'file_path',
  Edit: 'file_path',
  Glob: 'pattern',
  Grep: 'pattern',
};

// shortenPath trims a path against the working directory, then the home
// directory. Anything else — including a value that is not a path at all —
// is returned untouched.
function shortenPath(v) {
  if (cwd && v === cwd) return '.';
  if (cwd && v.startsWith(cwd + '/')) return v.slice(cwd.length + 1);
  if (home && v.startsWith(home + '/')) return '~/' + v.slice(home.length + 1);
  return v;
}

// toolArg reduces a tool call's JSON input to the one argument that
// identifies it. Input that isn't a JSON object (an MCP tool sending a bare
// string, say) is shown as-is rather than swallowed.
function toolArg(name, input) {
  let val = input;
  try {
    const args = JSON.parse(input);
    if (args && typeof args === 'object' && !Array.isArray(args)) {
      const keys = Object.keys(args);
      const primary = PRIMARY_ARG[name];
      const key = primary && keys.includes(primary) ? primary : keys[0];
      val = key === undefined ? '' : args[key];
    }
  } catch (_) { /* not JSON: fall through to the raw input */ }
  if (typeof val !== 'string') val = JSON.stringify(val);
  return shortenPath(val);
}

function toolCall(name, input, max) {
  return name + '(' + brief(toolArg(name, input), max) + ')';
}

function brief(s, n) {
  s = (s || '').replace(/\s+/g, ' ').trim();
  return s.length > n ? s.slice(0, n) + '…' : s;
}

const chat = document.getElementById('chat');
const queryEl = document.getElementById('query');
const sendBtn = document.getElementById('send-btn');
const cancelBtn = document.getElementById('cancel-btn');
const statusEl = document.getElementById('status');

let running = false;
let streamEl = null;
let thinkEl = null;
let thinkStart = 0;    // ms epoch the current thinking block began
let thinkTimer = null; // live counter interval; non-null only while ticking
let thinkHeadEl = null;    // the 'Thinking for …' line
let thinkPreviewEl = null; // rolling window onto the reasoning text
let thinkText = '';        // tail of the reasoning text, capped

// Enough to fill three wrapped rows at any width, so the window stays full
// while the retained string stays bounded over a long thinking block.
const THINK_PREVIEW_CHARS = 2000;
let inputTokens = 0;
let outputTokens = 0;
let costUSD = null;
let contextUsed = 0;   // tokens in the main agent's last request
let contextWindow = 0; // 0 = unknown, gauge hidden
let statusNote = '';   // transient left-hand note (tool phase)
let cwd = '';          // for shortening tool-call paths
let home = '';
let visionOK = false;
let pendingImages = []; // {media_type, data}

const attachmentsEl = document.getElementById('attachments');

async function refreshState() {
  try {
    const res = await fetch('/state');
    const d = await res.json();
    visionOK = !!d.vision;
    contextWindow = d.context_window || 0;
    cwd = d.cwd || '';
    home = d.home || '';
    renderStatus();
    if (!visionOK && pendingImages.length) {
      pendingImages = [];
      renderAttachments();
    }
  } catch(_) {}
}
refreshState();

function renderAttachments() {
  attachmentsEl.textContent = '';
  attachmentsEl.classList.toggle('has-items', pendingImages.length > 0);
  pendingImages.forEach((img, i) => {
    const chip = document.createElement('div');
    chip.className = 'chip';
    const thumb = document.createElement('img');
    thumb.src = 'data:' + img.media_type + ';base64,' + img.data;
    const rm = document.createElement('button');
    rm.textContent = '×';
    rm.onclick = () => {
      pendingImages = pendingImages.filter((_, j) => j !== i);
      renderAttachments();
    };
    chip.appendChild(thumb);
    chip.appendChild(rm);
    attachmentsEl.appendChild(chip);
  });
}

function attachImage(file) {
  if (!file || !file.type.startsWith('image/')) return;
  if (!visionOK) {
    addMsg('error', 'current model does not accept images');
    return;
  }
  const reader = new FileReader();
  reader.onload = () => {
    const data = reader.result.split(',', 2)[1]; // strip data: URI prefix
    pendingImages = pendingImages.concat({media_type: file.type, data});
    renderAttachments();
  };
  reader.readAsDataURL(file);
}

queryEl.addEventListener('paste', e => {
  const items = e.clipboardData && e.clipboardData.items;
  if (!items) return;
  for (const item of items) {
    if (item.kind === 'file' && item.type.startsWith('image/')) {
      e.preventDefault();
      attachImage(item.getAsFile());
    }
  }
});

document.addEventListener('dragover', e => e.preventDefault());
document.addEventListener('drop', e => {
  e.preventDefault();
  for (const f of e.dataTransfer.files) attachImage(f);
});

// autosize grows the composer with its content: two rows until the text
// needs a third, then up to seven, scrolling beyond that. Counts wrapped
// rows, not newlines, by measuring scrollHeight against the line height.
const QUERY_MIN_ROWS = 2;
const QUERY_MAX_ROWS = 7;

function autosize() {
  const cs = getComputedStyle(queryEl);
  const line = parseFloat(cs.lineHeight);
  const pad = parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom);
  const border = parseFloat(cs.borderTopWidth) + parseFloat(cs.borderBottomWidth);

  // Growing the composer shrinks the chat pane, which would slide the
  // newest message out of view; re-pin it if it was already at the bottom.
  const pinned = chat.scrollHeight - chat.scrollTop - chat.clientHeight < 4;

  queryEl.style.height = 'auto'; // collapse first so scrollHeight is content-sized
  const rows = Math.max(QUERY_MIN_ROWS, Math.min(QUERY_MAX_ROWS,
    Math.round((queryEl.scrollHeight - pad) / line)));
  queryEl.style.height = (rows * line + pad + border) + 'px';

  if (pinned) chat.scrollTop = chat.scrollHeight;
}

queryEl.addEventListener('input', autosize);
window.addEventListener('resize', autosize); // width changes rewrap the text
autosize();

function addMsg(cls, text) {
  const el = document.createElement('div');
  el.className = 'msg ' + cls;
  el.textContent = text;
  chat.appendChild(el);
  chat.scrollTop = chat.scrollHeight;
  return el;
}

// renderStatus redraws the whole status bar from current state: an
// optional note, the context gauge, and cumulative tokens and cost. The
// gauge tracks the last request's prompt size, which is the live context
// depth; the token counters stay cumulative for the turn.
const CTX_CELLS = 20;

function renderStatus() {
  statusEl.textContent = '';
  if (statusNote) statusEl.append(statusNote + ' ');

  if (contextWindow > 0) {
    const pct = Math.min(100, Math.round(contextUsed / contextWindow * 100));
    const filled = Math.min(CTX_CELLS, Math.round(pct / 100 * CTX_CELLS));
    const gauge = document.createElement('span');
    gauge.className = 'ctx' + (pct >= 90 ? ' crit' : pct >= 70 ? ' warn' : '');
    gauge.textContent = '[' + '█'.repeat(filled) + '░'.repeat(CTX_CELLS - filled) + '] ' + pct + '%';
    statusEl.append(gauge, '  ' + fmtTokens(contextUsed) + '/' + fmtTokens(contextWindow));
  }

  let tk = fmtTokens(inputTokens) + '↑ ' + fmtTokens(outputTokens) + '↓';
  if (costUSD != null) tk += ' $' + costUSD.toFixed(4);
  statusEl.append(contextWindow > 0 ? ' · ' + tk : tk);
}

function setRunning(v) {
  running = v;
  sendBtn.disabled = v;
  cancelBtn.style.display = v ? 'inline-block' : 'none';
  queryEl.disabled = v;
  if (!v) queryEl.focus();
}

function finalizeStream() {
  if (streamEl) { streamEl.classList.remove('streaming'); renderInto(streamEl); streamEl = null; }
}
// finalizeThinking closes the current thinking block: the live counter stops
// and settles into its final duration. Called whenever thinking gives way to
// something else — text, a tool call, the answer, or the turn ending — so each
// block is timed on its own rather than one running total per turn.
function finalizeThinking() {
  if (!thinkEl) return;
  if (thinkTimer !== null) {
    clearInterval(thinkTimer);
    thinkTimer = null;
    // Assigning textContent replaces the head and preview children, so the
    // reasoning text goes with them — the duration is all that remains.
    thinkEl.textContent = 'Thought for ' + fmtDuration(Date.now() - thinkStart);
  }
  thinkEl = thinkHeadEl = thinkPreviewEl = null;
  thinkText = '';
}

// tickThinking repaints the live counter. 100ms keeps the sub-second readout
// (fmtDuration reports ms below 1s) from looking frozen.
function tickThinking() {
  if (thinkHeadEl) thinkHeadEl.textContent = 'Thinking for ' + fmtDuration(Date.now() - thinkStart) + '…';
}

// SSE
const es = new EventSource('/events');

es.addEventListener('text_delta', e => {
  finalizeThinking();
  if (!streamEl) {
    streamEl = addMsg('streaming', '');
  }
  streamEl.textContent += e.data;
  chat.scrollTop = chat.scrollHeight;
});

es.addEventListener('thinking_delta', e => {
  if (!thinkEl) {
    thinkEl = addMsg('thinking', '');
    if (!DEBUG) {
      // Two children: the timer line, and a fixed-height window beneath it.
      thinkHeadEl = document.createElement('span');
      thinkPreviewEl = document.createElement('div');
      thinkPreviewEl.className = 'preview';
      thinkEl.append(thinkHeadEl, thinkPreviewEl);
      thinkText = '';
      thinkStart = Date.now();
      thinkTimer = setInterval(tickThinking, 100);
      tickThinking();
    }
  }
  if (DEBUG) {
    // Debug shows the complete reasoning, permanently: no timer, no window.
    thinkEl.textContent += e.data;
    chat.scrollTop = chat.scrollHeight;
    return;
  }
  thinkText = (thinkText + e.data).slice(-THINK_PREVIEW_CHARS);
  thinkPreviewEl.textContent = thinkText;
  // The browser does the wrapping; scrolling to the bottom of a clipped box
  // is what makes the window show the LAST three rows rather than the first.
  thinkPreviewEl.scrollTop = thinkPreviewEl.scrollHeight;
  chat.scrollTop = chat.scrollHeight;
});

function agentTag(d) {
  return d.agent ? '[' + d.agent + '] ' : '';
}

// SSE payloads are wire envelopes: {v, type, ts, runner_id, data, agent},
// with the event's fields under .data and the server's subagent label in
// .agent.
es.addEventListener('tool_execution.started', e => {
  finalizeStream();
  finalizeThinking();
  const d = JSON.parse(e.data);
  const cls = 'tool' + (d.agent ? ' sub' : '');
  // Same rendering either way; debug just gets a longer leash on the
  // argument. The untouched JSON is still in the trace-level log file.
  addMsg(cls, (DEBUG ? '⚙ ' : '⏺ ') + agentTag(d) + toolCall(d.data.tool_name, d.data.input, DEBUG ? 500 : 60));
});

// diffBlock renders a unified diff with per-line colouring. Content is set
// through textContent, never innerHTML: these lines are file contents.
function diffBlock(text) {
  const el = document.createElement('div');
  el.className = 'diff';
  for (const line of text.split('\n')) {
    const row = document.createElement('div');
    if (line.startsWith('+++') || line.startsWith('---')) row.className = 'meta';
    else if (line.startsWith('@@')) row.className = 'hunk';
    else if (line.startsWith('+')) row.className = 'add';
    else if (line.startsWith('-')) row.className = 'del';
    row.textContent = line;
    el.appendChild(row);
  }
  return el;
}

// diffSummary is the ⎿ line for an Edit/Write result: counts, plus why the
// body is missing when it is. Returns null for tools that carry no diff.
function diffSummary(meta) {
  if (!meta || meta.diff_added === undefined) return null;
  let s = '+' + meta.diff_added + ' -' + meta.diff_removed;
  if (meta.diff_omitted) s += ' (diff omitted: ' + meta.diff_omitted + ')';
  return s;
}

// A diff of at most this many lines is shown expanded; longer ones start
// collapsed behind the summary line. Mirrors maxInlineDiffLines in
// internal/features/builtins/diff.go.
const INLINE_DIFF_LINES = 20;

// attachDiff hangs the diff body off a result line — expanded when short,
// click-to-expand when long.
function attachDiff(el, text) {
  if (!text) return;
  if (text.split('\n').length <= INLINE_DIFF_LINES) {
    el.appendChild(diffBlock(text));
    return;
  }
  const toggle = document.createElement('span');
  toggle.className = 'expand';
  toggle.textContent = ' show diff';
  let body = null;
  toggle.onclick = () => {
    if (body) { body.remove(); body = null; toggle.textContent = ' show diff'; return; }
    body = diffBlock(text);
    el.appendChild(body);
    toggle.textContent = ' hide diff';
  };
  el.appendChild(toggle);
}

// outputPreview is the body of a plain result line: up to three lines of
// output verbatim, with a '+ N lines' tail when there are more. Blank lines
// don't count. Continuations line up under the ⎿ marker's text.
function outputPreview(output) {
  const lines = (output || '').split('\n').filter(l => l.trim()).map(l => brief(l, 80));
  if (lines.length === 0) return '0 lines';
  const rest = lines.length - 3;
  const head = lines.slice(0, 3).join('\n   ');
  return rest <= 0 ? head : head + '\n   + ' + rest + ' line' + (rest === 1 ? '' : 's');
}

function toolResult(d, output, isError) {
  const meta = d.data.metadata;
  const summary = isError ? null : diffSummary(meta);
  if (!DEBUG) {
    // Claude Code-style continuation line under the ⏺ call line: outcome
    // only, no tool output — except an Edit/Write diff, which is the whole
    // point of the line.
    const el = addMsg('tool result', isError
      ? '⎿  error: ' + brief(output, 80)
      : '⎿ ' + (summary || outputPreview(output)));
    if (summary) attachDiff(el, meta.diff);
    return;
  }
  const lines = (output || '').split('\n').slice(0, 10);
  const prefix = isError ? '✗ ' : '✓ ';
  const el = addMsg('tool' + (d.agent ? ' sub' : ''),
    prefix + agentTag(d) + toolCall(d.data.tool_name, d.data.input, 500) + '\n' + lines.join('\n'));
  if (summary) attachDiff(el, meta.diff);
}

es.addEventListener('tool.succeeded', e => {
  const d = JSON.parse(e.data);
  toolResult(d, d.data.output, false);
});

es.addEventListener('tool.failed', e => {
  const d = JSON.parse(e.data);
  toolResult(d, d.data.error, true);
});

es.addEventListener('approval.requested', e => {
  finalizeStream();
  const d = JSON.parse(e.data);
  const arg = toolArg(d.data.tool_name, d.data.input);
  const el = addMsg('approval', '⚠ ' + agentTag(d) + d.data.tool_name + ' wants to run:\n' +
    (arg.length > 500 ? arg.slice(0, 500) + '…' : arg));
  // For bash the reason is the shell analysis: one line per mutating
  // segment (class, command, why), a parse error, or a category denial —
  // the decision is made against that rather than the raw command.
  const isBash = (d.data.tool_name || '').toLowerCase() === 'bash';
  if (isBash && d.data.reason) {
    const why = document.createElement('pre');
    why.className = 'reason';
    why.textContent = d.data.reason;
    el.appendChild(why);
  }

  const approve = document.createElement('button');
  approve.textContent = 'approve';
  const deny = document.createElement('button');
  deny.textContent = 'deny';
  deny.className = 'deny';
  // "allow always" / "allow for session" are bash-only: both apply the glob
  // to this session's allow list so matching commands stop prompting; only
  // "always" also writes it to the settings file. Prefilled locally with the
  // command's first word + ' *', then refined by /suggest, which knows the
  // live allow list and proposes a rule for the first segment that would
  // still prompt.
  let always, session, pattern;
  if (isBash) {
    pattern = document.createElement('input');
    pattern.className = 'pattern';
    pattern.value = suggestGlob(d.data.input);
    always = document.createElement('button');
    always.textContent = 'allow always';
    session = document.createElement('button');
    session.textContent = 'allow for session';
    fetch('/suggest', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({call_id: d.data.call_id}),
    }).then(r => r.ok ? r.json() : null).then(sg => {
      if (!sg || pattern.disabled) return; // answered already
      if (sg.glob) { pattern.value = sg.glob; return; }
      // Nothing worth allowlisting (a file write, or already covered): say
      // so rather than offering a glob that would grant more than it looks.
      pattern.value = '';
      pattern.placeholder = sg.reason || 'no glob suggested';
    }).catch(() => { /* advisory: the local suggestion stands */ });
  }
  // Edit/Write: fetch the diff the call would produce, so the decision is
  // made against the change rather than the raw arguments.
  if (d.data.tool_name === 'Edit' || d.data.tool_name === 'Write') {
    fetch('/preview', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({call_id: d.data.call_id}),
    }).then(r => r.ok ? r.json() : null).then(p => {
      if (!p) return;
      if (p.error) { el.append('\n(no preview: ' + p.error + ')'); return; }
      el.append('\n' + diffSummary({
        diff_added: p.added, diff_removed: p.removed, diff_omitted: p.omitted,
      }));
      attachDiff(el, p.diff);
    }).catch(() => { /* preview is advisory: never block the decision */ });
  }

  const buttons = () => isBash ? [approve, deny, always, session] : [approve, deny];
  const decide = async (approved, allow, scope) => {
    buttons().forEach(b => b.disabled = true);
    if (pattern) pattern.disabled = true;
    try {
      const res = await fetch('/approve', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({call_id: d.data.call_id, approved, allow, scope}),
      });
      if (!res.ok) throw new Error('approve failed: ' + res.status + ' ' + await res.text());
      const verdict = !allow ? (approved ? 'approved' : 'denied')
        : scope === 'session' ? 'allowing ' + allow + ' this session' : 'always allowing ' + allow;
      el.append(' → ' + verdict);
    } catch(err) {
      // Leave the request answerable: the write may have failed.
      buttons().forEach(b => b.disabled = false);
      if (pattern) pattern.disabled = false;
      addMsg('error', err.message);
    }
  };
  approve.onclick = () => decide(true);
  deny.onclick = () => decide(false);
  el.appendChild(document.createElement('br'));
  el.appendChild(approve);
  el.appendChild(deny);
  if (isBash) {
    always.onclick = () => {
      const p = pattern.value.trim();
      if (p) decide(true, p, 'always');
    };
    session.onclick = () => {
      const p = pattern.value.trim();
      if (p) decide(true, p, 'session');
    };
    el.appendChild(always);
    el.appendChild(session);
    el.appendChild(pattern);
  }
  chat.scrollTop = chat.scrollHeight;
});

// suggestGlob is the local fallback prefill for a bash call: the command's
// first word plus ' *'. /suggest replaces it with an expression-aware rule
// when it answers. Only a suggestion — the field is editable, and a chained
// command needs every expression covered before it stops prompting.
function suggestGlob(input) {
  let cmd = '';
  try { cmd = (JSON.parse(input).command || '').trim(); } catch(e) { return ''; }
  const word = cmd.split(/\s/)[0];
  return word ? word + ' *' : '';
}

es.addEventListener('subagent.started', e => {
  finalizeStream();
  const d = JSON.parse(e.data);
  addMsg('subagent', '⧉ ' + d.data.agent_id + ' spawned: ' + brief(d.data.prompt, 200));
});

es.addEventListener('subagent.stopped', e => {
  finalizeStream();
  const d = JSON.parse(e.data);
  addMsg('subagent', '⧉ ' + d.data.agent_id + ' done (' + fmtDuration(d.data.duration_ms) + ')');
});

es.addEventListener('tool.progress', e => {
  const d = JSON.parse(e.data);
  if (DEBUG && d.data.detail.trim()) {
    addMsg('tool-progress', '▸ ' + d.data.phase + '\n' + d.data.detail);
  }
  statusNote = '⚙ ' + d.data.tool_name + ' → ' + d.data.phase;
  renderStatus();
});

es.addEventListener('llm.response', e => {
  const d = JSON.parse(e.data);
  inputTokens += d.data.input_tokens;
  outputTokens += d.data.output_tokens;
  if (!d.agent) {
    // Anthropic reports cached prompt tokens outside input_tokens; other
    // protocols leave those fields at 0, so the sum is the prompt size
    // either way.
    contextUsed = d.data.input_tokens +
      (d.data.cache_read_input_tokens || 0) +
      (d.data.cache_creation_input_tokens || 0);
    renderStatus();
  }
});

es.addEventListener('cost', e => {
  const d = JSON.parse(e.data);
  costUSD = d.cost_usd;
  renderStatus();
});

es.addEventListener('steering.injected', e => {
  const d = JSON.parse(e.data);
  addMsg('system', '↪ steering injected: ' + d.data.message);
});

es.addEventListener('llm.retry', e => {
  const d = JSON.parse(e.data);
  addMsg('system', '↻ llm retry ' + d.data.attempt + '/' + d.data.max_retries + ': ' + d.data.error);
});

es.addEventListener('model.changed', e => {
  const d = JSON.parse(e.data);
  addMsg('system', '⇄ model: ' + d.data.from + ' → ' + d.data.to);
  refreshState();
});

es.addEventListener('thinking.changed', e => {
  const d = JSON.parse(e.data);
  addMsg('system', '💭 thinking ' + (d.data.enabled ? 'on' : 'off'));
});

es.addEventListener('images.attached', e => {
  const d = JSON.parse(e.data);
  addMsg('system', '🖼 ' + d.data.count + ' image' + (d.data.count > 1 ? 's' : '') + ' attached');
});

es.addEventListener('queued', e => {
  const d = JSON.parse(e.data);
  addMsg('system', '⏳ queued at position ' + d.position + ': ' + d.query);
});

function fmtDuration(ms) {
  if (ms >= 1000) return (ms / 1000).toFixed(1) + 's';
  return ms + 'ms';
}

es.addEventListener('answer', e => {
  const d = JSON.parse(e.data);
  finalizeThinking();
  if (streamEl) {
    streamEl.classList.remove('streaming');
    renderInto(streamEl);
    streamEl = null;
  } else if (d.text) {
    renderInto(addMsg('assistant', d.text));
  }
});

es.addEventListener('canceled', e => {
  finalizeStream();
  finalizeThinking();
  addMsg('system', '⊘ canceled');
});

es.addEventListener('error', e => {
  try {
    const d = JSON.parse(e.data);
    addMsg('error', d.error);
  } catch(_) {
    addMsg('error', e.data);
  }
});

es.addEventListener('status', e => {
  const d = JSON.parse(e.data);
  if (d.state === 'running') {
    // No note while running: the old 'thinking…' text was set once here and
    // survived every tool call and the streamed answer. Per-block timing now
    // lives on the ✻ line; this only clears a leftover tool phase.
    statusNote = '';
    renderStatus();
  } else {
    statusNote = '';
    finalizeThinking(); // backstop: no path ends a turn with the timer running
    renderStatus();
    setRunning(false);
  }
});

function fmtTokens(n) {
  const short = v => String(parseFloat(v.toFixed(1))); // 82.0 → 82, 82.4 → 82.4
  if (n >= 1e6) return short(n / 1e6) + 'M';
  if (n >= 1e3) return short(n / 1e3) + 'k';
  return n.toString();
}

// ---- slash commands -------------------------------------------------
//
// Action commands are a client concern: each maps to an HTTP endpoint the
// server already exposes. A query that is not a known command goes to
// /query unchanged, so the harness's own file-based prompt templates
// (/name from WithPromptTemplatesDir) still reach the agent.

async function post(path, body) {
  const res = await fetch(path, {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(body || {}),
  });
  const text = await res.text();
  if (!res.ok) throw new Error(text || res.statusText);
  try { return JSON.parse(text); } catch (_) { return {}; }
}

async function getJSON(path) {
  const res = await fetch(path);
  if (!res.ok) throw new Error(await res.text() || res.statusText);
  return res.json();
}

function note(text) { addMsg('system', '↳ ' + text); }

const COMMANDS = [
  {
    name: '/clear', args: '', desc: 'reset the context and start a new conversation',
    run: async () => {
      const r = await post('/clear');
      chat.textContent = '';           // the transcript went with the context
      inputTokens = outputTokens = 0;
      costUSD = null;
      contextUsed = 0;
      renderStatus();
      note(r.status || 'context cleared');
      refreshState();
    },
  },
  {
    name: '/compact', args: '[instructions]', desc: 'compress the context now',
    run: async a => note((await post('/compact', {instructions: a})).status || 'compacted'),
  },
  {
    name: '/cost', args: '', desc: 'token and dollar totals for this conversation',
    run: async () => {
      const c = await getJSON('/stats');
      const money = c.cost_usd == null ? 'unpriced' : '$' + c.cost_usd.toFixed(4);
      note(c.calls + ' calls · ' + fmtTokens(c.input_tokens) + '↑ ' +
        fmtTokens(c.output_tokens) + '↓ · ' + money);
    },
  },
  {
    name: '/model', args: '[provider/name]', desc: 'switch model, or list what is available',
    run: async a => {
      if (!a) {
        const m = await getJSON('/models');
        note('current: ' + m.current + '\n' + (m.models || []).join('\n'));
        return;
      }
      note((await post('/model', {model: a})).status || 'model set');
      refreshState();
    },
  },
  {
    name: '/thinking', args: 'on|off', desc: 'turn model reasoning on or off',
    run: async a => {
      const on = /^(on|true|1)$/i.test(a);
      if (!on && !/^(off|false|0)$/i.test(a)) throw new Error('usage: /thinking on|off');
      note((await post('/thinking', {enabled: on})).status || 'thinking set');
    },
  },
  {
    name: '/sessions', args: '', desc: 'list conversations recorded for this directory',
    run: async () => {
      const d = await getJSON('/sessions');
      const rows = (d.sessions || []).map(x =>
        (x.active ? '* ' : '  ') + x.conversation_id + '  ' + x.entries + ' entries  ' +
        (x.name || x.model) + '  ' + x.modified);
      note(rows.length ? rows.join('\n') : 'no sessions recorded here');
    },
  },
  {
    name: '/resume', args: '<id>', desc: 'load a recorded conversation into this session',
    run: async a => {
      if (!a) throw new Error('usage: /resume <conversation-id>  (see /sessions)');
      const r = await post('/resume', {conversation_id: a});
      chat.textContent = '';
      inputTokens = outputTokens = 0;
      costUSD = null;
      contextUsed = 0;
      renderStatus();
      note(r.status || 'resumed');
      refreshState();
    },
  },
  {
    name: '/help', args: '', desc: 'list these commands',
    run: async () => note(COMMANDS.map(c =>
      (c.name + ' ' + c.args).padEnd(26) + c.desc).join('\n')),
  },
];

// parseCommand splits a submitted line into a command and its argument
// string. Returns null when the line is not a slash command at all, and a
// {name} with no match when it looks like one but is not ours — the caller
// decides whether that is an error or a prompt template.
function parseCommand(line) {
  const m = /^\/([A-Za-z0-9_-]+)\s*([\s\S]*)$/.exec(line.trim());
  if (!m) return null;
  const name = '/' + m[1];
  return {name, args: m[2].trim(), cmd: COMMANDS.find(c => c.name === name)};
}

// matchCommands filters the menu as the user types. Only a first "word"
// still being typed counts — once there is a space, the command is chosen
// and the menu gets out of the way.
function matchCommands(value) {
  const m = /^\/([A-Za-z0-9_-]*)$/.exec(value);
  if (!m) return [];
  return COMMANDS.filter(c => c.name.startsWith('/' + m[1]));
}

async function runCommand(parsed) {
  try {
    await parsed.cmd.run(parsed.args);
  } catch (err) {
    addMsg('error', parsed.name + ': ' + err.message);
  }
}

// ---- type-ahead menu ------------------------------------------------
const menuEl = document.getElementById('cmdmenu');
let menuItems = [];
let menuSel = 0;

function renderMenu() {
  menuEl.textContent = '';
  menuEl.classList.toggle('open', menuItems.length > 0);
  menuItems.forEach((c, i) => {
    const row = document.createElement('div');
    row.className = 'item' + (i === menuSel ? ' sel' : '');
    const name = document.createElement('span');
    name.className = 'name';
    name.textContent = (c.name + ' ' + c.args).trim();
    const desc = document.createElement('span');
    desc.className = 'desc';
    desc.textContent = c.desc;
    row.append(name, desc);
    // mousedown, not click: the composer must not lose focus first.
    row.addEventListener('mousedown', e => { e.preventDefault(); acceptMenu(i); });
    menuEl.appendChild(row);
  });
}

function updateMenu() {
  menuItems = running ? [] : matchCommands(queryEl.value);
  menuSel = 0;
  renderMenu();
}

function closeMenu() {
  menuItems = [];
  renderMenu();
}

// acceptMenu completes the composer to the chosen command. Commands that
// take arguments keep the composer open with a trailing space; the rest
// are submitted straight away.
function acceptMenu(i) {
  const c = menuItems[i];
  if (!c) return;
  closeMenu();
  queryEl.value = c.name + (c.args ? ' ' : '');
  autosize();
  queryEl.focus();
  if (!c.args) send();
}

queryEl.addEventListener('input', updateMenu);
queryEl.addEventListener('blur', closeMenu);

async function send() {
  const q = queryEl.value.trim();
  if (!q || running) return;

  // Action commands never reach the agent. An unknown /name still does:
  // the harness's own prompt templates live in that namespace.
  const parsed = parseCommand(q);
  if (parsed && parsed.cmd) {
    closeMenu();
    addMsg('user', q);
    queryEl.value = '';
    autosize();
    await runCommand(parsed);
    return;
  }

  const images = pendingImages;
  pendingImages = [];
  renderAttachments();
  addMsg('user', q + (images.length ? ' [' + images.length + ' image' + (images.length > 1 ? 's' : '') + ']' : ''));
  queryEl.value = '';
  autosize();
  setRunning(true);
  streamEl = null;
  thinkEl = null;

  const body = {query: q};
  if (images.length) body.images = images;
  try {
    const res = await fetch('/query', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      const txt = await res.text();
      addMsg('error', txt);
      setRunning(false);
    }
  } catch(e) {
    addMsg('error', e.message);
    setRunning(false);
  }
}

async function cancel() {
  try { await fetch('/cancel', {method: 'POST'}); } catch(_) {}
}

queryEl.addEventListener('keydown', e => {
  if (menuItems.length) {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      menuSel = (menuSel + (e.key === 'ArrowDown' ? 1 : menuItems.length - 1)) % menuItems.length;
      renderMenu();
      return;
    }
    if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) {
      e.preventDefault();
      acceptMenu(menuSel);
      return;
    }
    if (e.key === 'Escape') {
      e.preventDefault();
      closeMenu();
      return;
    }
  }
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault();
    send();
  }
});
</script>

</body>
</html>`
