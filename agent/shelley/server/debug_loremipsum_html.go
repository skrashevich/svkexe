package server

// debugLoremIpsumHTML renders the /debug/loremipsum landing page. The
// __BANNER__ and __ROWS__ placeholders are substituted with an optional error
// banner and the preset table rows.
const debugLoremIpsumHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Shelley — Synthetic Conversation Generator</title>
<link rel="stylesheet" href="/styles.css">
<style>
html, body { height: auto !important; overflow: auto !important; }
body {
  padding: 2rem;
  max-width: 760px;
  margin: 0 auto;
  background: var(--bg-base) !important;
  color: var(--text-primary);
}
h1 { font-size: 1.5rem; margin-bottom: 0.25rem; }
.subtitle { color: var(--text-secondary); margin-bottom: 1.5rem; }
.banner {
  background: #fef2f2; color: #991b1b; border: 1px solid #fecaca;
  border-radius: 8px; padding: 0.75rem 1rem; margin-bottom: 1.5rem;
}
table { border-collapse: collapse; width: 100%; margin-bottom: 2rem; }
th, td { text-align: left; padding: 0.6rem 0.75rem; border-bottom: 1px solid var(--border); vertical-align: middle; }
thead th { color: var(--text-secondary); font-weight: 600; font-size: 0.85rem; }
td.name { font-weight: 600; }
td.turns, td.msgs { color: var(--text-secondary); font-variant-numeric: tabular-nums; }
td:last-child { text-align: right; }
button {
  background: var(--primary, #2563eb); color: #fff; border: none;
  border-radius: 6px; padding: 0.4rem 0.9rem; font-size: 0.9rem; cursor: pointer;
}
button:hover { filter: brightness(1.08); }
form { margin: 0; }
.custom {
  border: 1px solid var(--border); border-radius: 8px; padding: 1rem 1.25rem;
  margin-bottom: 2rem; background: var(--bg-secondary, rgba(127,127,127,0.05));
}
.custom h2 { font-size: 1rem; margin: 0 0 0.5rem; }
.custom .row { display: flex; gap: 0.5rem; align-items: center; flex-wrap: wrap; }
.custom input[type=text], .custom input[type=number] {
  padding: 0.4rem 0.6rem; border: 1px solid var(--border); border-radius: 6px;
  background: var(--bg-base); color: var(--text-primary); min-width: 8rem;
}
.custom label { color: var(--text-secondary); font-size: 0.9rem; }
.note { color: var(--text-secondary); font-size: 0.85rem; }
code { background: var(--bg-secondary, rgba(127,127,127,0.12)); padding: 0.1rem 0.35rem; border-radius: 4px; }
</style>
</head>
<body>
<h1>Synthetic conversation generator</h1>
<div class="subtitle">Creates a throwaway conversation containing every message and tool-call shape the UI renders, for load/render performance testing.</div>
__BANNER__
<p class="note" style="margin-bottom:1.5rem">Generating writes a new conversation to the database. A &ldquo;turn&rdquo; is one user prompt plus the agent&rsquo;s reply and tool calls (~4 messages). Larger sizes can take a while and periodically compact (creating generation changes and distillation summaries).</p>

<table>
<thead><tr><th>Preset</th><th>Size</th><th>Approx.</th><th></th></tr></thead>
<tbody>
__ROWS__
</tbody>
</table>

<div class="custom">
<h2>Custom size</h2>
<form method="post" class="row">
  <label for="size">Turns</label>
  <input type="number" id="size" name="size" min="1" max="100000" value="100" required>
  <label for="model">Model (optional)</label>
  <input type="text" id="model" name="model" placeholder="default">
  <button type="submit">Generate</button>
</form>
<p class="note" style="margin-top:0.75rem;margin-bottom:0">1&ndash;100,000 turns. You can also POST with <code>?json=1</code> to get the conversation id back instead of a redirect.</p>
</div>
</body>
</html>`
