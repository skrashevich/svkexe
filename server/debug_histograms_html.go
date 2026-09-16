package server

// debugHistogramsHTML renders the histograms page. The __STATS_JSON__
// placeholder is replaced with the JSON stats blob, embedded into the page so
// the charts render without a second round-trip. Vega/Vega-Lite are loaded
// from a CDN.
const debugHistogramsHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Shelley Conversation Histograms</title>
<link rel="stylesheet" href="/styles.css">
<script src="https://cdn.jsdelivr.net/npm/vega@5"></script>
<script src="https://cdn.jsdelivr.net/npm/vega-lite@5"></script>
<script src="https://cdn.jsdelivr.net/npm/vega-embed@6"></script>
<style>
html, body { height: auto !important; overflow: auto !important; }
body {
  padding: 2rem;
  max-width: 1000px;
  margin: 0 auto;
  background: var(--bg-base) !important;
  color: var(--text-primary);
}
h1 { font-size: 1.5rem; margin-bottom: 0.25rem; }
h2 { font-size: 1.15rem; margin: 2rem 0 0.75rem; }
.subtitle { color: var(--text-secondary); margin-bottom: 1.5rem; }
.cards { display: flex; flex-wrap: wrap; gap: 1rem; margin-bottom: 1rem; }
.card {
  background: var(--bg-secondary, rgba(127,127,127,0.08));
  border: 1px solid var(--border, #ccc);
  border-radius: 8px;
  padding: 1rem 1.25rem;
  min-width: 150px;
}
.card .num { font-size: 1.6rem; font-weight: 600; color: var(--text-primary); }
.card .lbl { color: var(--text-secondary); font-size: 0.85rem; }
table { border-collapse: collapse; width: 100%; margin-bottom: 1rem; font-size: 0.9rem; }
th, td { text-align: right; padding: 0.4rem 0.75rem; border-bottom: 1px solid var(--border); }
th:first-child, td:first-child { text-align: left; }
thead th { color: var(--text-secondary); font-weight: 600; }
.chart { display: block; width: 100%; background: #fff; border-radius: 8px; padding: 0.5rem; margin-bottom: 1rem; box-sizing: border-box; }
.chart .vega-embed, .chart .vega-embed .marks { width: 100% !important; }
.note { color: var(--text-secondary); font-size: 0.85rem; margin-bottom: 2rem; }
code { background: var(--bg-secondary, rgba(127,127,127,0.12)); padding: 0.1rem 0.35rem; border-radius: 4px; }
</style>
</head>
<body>
<h1>Conversation size histograms</h1>
<div class="subtitle">Size distribution of conversations in this database. Informs the <code>/debug/loremipsum</code> presets.</div>

<div class="cards" id="cards"></div>

<h2>Messages per conversation</h2>
<table id="msg-pct"></table>
<div class="chart" id="msg-chart"></div>

<h2>Stored bytes per conversation</h2>
<div class="note">Sum of the <code>llm_data</code>, <code>user_data</code>, and <code>display_data</code> JSON columns — the payload the client loads and renders.</div>
<table id="bytes-pct"></table>
<div class="chart" id="bytes-chart"></div>

<h2>Generations (compactions)</h2>
<div class="note">Conversations bucketed by their current generation. Generation 1 means never compacted; higher means N-1 compactions.</div>
<div class="chart" id="gen-chart"></div>

<h2>Message types</h2>
<div class="chart" id="type-chart"></div>

<script>
const STATS = __STATS_JSON__;

function fmtInt(n) { return n.toLocaleString(); }
function fmtBytes(n) {
  if (n < 1024) return n + ' B';
  if (n < 1024*1024) return (n/1024).toFixed(1) + ' KB';
  if (n < 1024*1024*1024) return (n/1024/1024).toFixed(1) + ' MB';
  return (n/1024/1024/1024).toFixed(2) + ' GB';
}

// Summary cards.
const cards = [
  ['Conversations', fmtInt(STATS.conversations)],
  ['Messages', fmtInt(STATS.messages)],
  ['Stored bytes', fmtBytes(STATS.bytes)],
];
document.getElementById('cards').innerHTML = cards.map(
  ([lbl, num]) => '<div class="card"><div class="num">' + num + '</div><div class="lbl">' + lbl + '</div></div>'
).join('');

// Percentile tables.
function pctTable(id, p, fmt) {
  const rows = [
    ['min', p.min], ['p50 (median)', p.p50], ['p90', p.p90],
    ['p95', p.p95], ['p99', p.p99], ['max', p.max],
    ['mean', Math.round(p.mean)],
  ];
  document.getElementById(id).innerHTML =
    '<thead><tr><th>percentile</th><th>value</th></tr></thead><tbody>' +
    rows.map(([k, v]) => '<tr><td>' + k + '</td><td>' + fmt(v) + '</td></tr>').join('') +
    '</tbody>';
}
pctTable('msg-pct', STATS.messages_percentiles, fmtInt);
pctTable('bytes-pct', STATS.bytes_percentiles, fmtBytes);

const chartBase = { width: 'container', height: 220, background: 'transparent' };

function histSpec(values, field, title, isBytes) {
  return Object.assign({}, chartBase, {
    $schema: 'https://vega.github.io/schema/vega-lite/v5.json',
    data: { values: values.map(v => ({ v: v })) },
    mark: { type: 'bar', tooltip: true },
    encoding: {
      x: {
        field: 'v', bin: { maxbins: 40 }, type: 'quantitative',
        title: title,
        axis: isBytes ? { format: '~s' } : {},
      },
      y: { aggregate: 'count', type: 'quantitative', title: 'conversations' },
    },
  });
}

function barSpec(data, xtitle) {
  return Object.assign({}, chartBase, {
    $schema: 'https://vega.github.io/schema/vega-lite/v5.json',
    data: { values: data },
    mark: { type: 'bar', tooltip: true },
    encoding: {
      x: { field: 'label', type: 'nominal', title: xtitle, sort: null },
      y: { field: 'count', type: 'quantitative', title: 'count' },
    },
  });
}

const embedOpts = { actions: false };
vegaEmbed('#msg-chart', histSpec(STATS.messages_per_conv, 'v', 'messages per conversation', false), embedOpts);
vegaEmbed('#bytes-chart', histSpec(STATS.bytes_per_conv, 'v', 'bytes per conversation', true), embedOpts);
vegaEmbed('#gen-chart', barSpec(STATS.generation_counts, 'generation'), embedOpts);
vegaEmbed('#type-chart', barSpec(STATS.type_counts, 'message type'), embedOpts);
</script>
</body>
</html>`
