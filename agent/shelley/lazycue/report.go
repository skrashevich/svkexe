package lazycue

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// artifactCollector accumulates per-step screenshots for a single test run
// and writes them to disk. It is safe for the sequential ExecuteSteps loop.
type artifactCollector struct {
	dir         string
	mu          sync.Mutex
	screenshots map[int]string // step index -> relative png filename
}

func newArtifactCollector(dir string) (*artifactCollector, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &artifactCollector{dir: dir, screenshots: map[int]string{}}, nil
}

// sink returns a screenshot sink suitable for Browser.SetScreenshotSink.
func (a *artifactCollector) sink() func(int, string, []byte) {
	return func(stepIndex int, action string, png []byte) {
		name := fmt.Sprintf("step-%02d-%s.png", stepIndex, sanitize(action))
		if err := os.WriteFile(filepath.Join(a.dir, name), png, 0o644); err != nil {
			return
		}
		a.mu.Lock()
		a.screenshots[stepIndex] = name
		a.mu.Unlock()
	}
}

// attach assigns captured screenshot paths back onto step results.
func (a *artifactCollector) attach(results []StepResult) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range results {
		if name, ok := a.screenshots[i]; ok {
			results[i].Screenshot = filepath.Join(a.dir, name)
		}
	}
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// WriteReport renders an HTML report for a set of test results, embedding
// per-step screenshots by relative path. The report is written to
// dir/index.html. Screenshot paths in results are made relative to dir.
func WriteReport(dir string, results []*TestResult) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString(reportHeader)

	// Summary stats.
	var pass, fail, cached, generated, healed int
	var totalCost float64
	var totalIn, totalOut int
	for _, r := range results {
		if r.Pass {
			pass++
		} else {
			fail++
		}
		switch r.Mode {
		case RunModeCached:
			cached++
		case RunModeGenerated:
			generated++
		case RunModeHealed:
			healed++
		}
		totalCost += r.EstimatedCost
		totalIn += r.InputTokens
		totalOut += r.OutputTokens
	}

	fmt.Fprintf(&b, `<header class="summary">
  <h1>lazycue report</h1>
  <div class="stats">
    <span class="stat pass">%d passed</span>
    <span class="stat fail">%d failed</span>
    <span class="stat">%d cached</span>
    <span class="stat">%d generated</span>
    <span class="stat">%d healed</span>
    <span class="stat">$%.3f &middot; %s in / %s out tok</span>
  </div>
  <div class="toolbar">
    <input type="search" id="filter" placeholder="Filter tests…" aria-label="Filter tests">
    <button type="button" data-act="expand">Expand all</button>
    <button type="button" data-act="collapse">Collapse all</button>
    <label class="toggle"><input type="checkbox" id="only-fail"> Failures only</label>
  </div>
</header>
`, pass, fail, cached, generated, healed, totalCost, commaInt(totalIn), commaInt(totalOut))

	b.WriteString(`<main id="tests">` + "\n")
	for i, r := range results {
		writeTestRow(&b, dir, i, r)
	}
	b.WriteString("</main>\n")

	b.WriteString(reportFooter)
	return os.WriteFile(filepath.Join(dir, "index.html"), []byte(b.String()), 0o644)
}

// writeTestRow renders one collapsible <details> row: a clickable summary line
// plus a body laid out with the video on the left and the per-step screenshots
// on the right. Failures start expanded; passes start collapsed.
func writeTestRow(b *strings.Builder, dir string, idx int, r *TestResult) {
	statusClass := "pass"
	statusText := "PASS"
	if !r.Pass {
		statusClass = "fail"
		statusText = "FAIL"
	}
	open := ""
	if !r.Pass {
		open = " open"
	}
	// Both the test name and description are filter targets (data-search).
	search := strings.ToLower(r.Name + " " + r.Description)
	nameChip := ""
	if r.Name != "" {
		nameChip = fmt.Sprintf(`<span class="tname">%s</span>`, html.EscapeString(r.Name))
	}
	fmt.Fprintf(b, `<details class="test %s" id="t%d" data-search="%s"%s>
  <summary>
    <span class="badge %s">%s</span>
    %s<span class="sumtext">%s</span>
    <span class="meta">%s v%d &middot; %s</span>
  </summary>
  <div class="body">
`, statusClass, idx, html.EscapeString(search), open,
		statusClass, statusText, nameChip, html.EscapeString(r.Description),
		html.EscapeString(string(r.Mode)), r.CacheVersion, r.TotalDuration.Round(time.Millisecond))

	// Left column: video (+ error / heal diagnostics beneath it).
	b.WriteString(`    <div class="left">` + "\n")
	if r.VideoPath != "" {
		rel := relPath(dir, r.VideoPath)
		fmt.Fprintf(b, `      <video controls preload="metadata" src="%s"></video>`+"\n", rel)
	} else {
		b.WriteString(`      <div class="no-video">no video</div>` + "\n")
	}
	if r.Error != "" {
		fmt.Fprintf(b, `      <div class="error">%s</div>`+"\n", html.EscapeString(r.Error))
	}
	writeHeal(b, r.Heal)
	b.WriteString("    </div>\n")

	// Right column: one card per step (instruction + screenshot).
	b.WriteString(`    <div class="shots">` + "\n")
	for i, s := range r.Steps {
		mark := "✓"
		cls := "ok"
		if !s.Pass {
			mark = "✗"
			cls = "bad"
		}
		fmt.Fprintf(b, `      <figure class="shot %s">`+"\n", cls)
		if s.Screenshot != "" {
			rel := relPath(dir, s.Screenshot)
			fmt.Fprintf(b, `        <a href="%s" target="_blank"><img loading="lazy" src="%s"></a>`+"\n", rel, rel)
		}
		fmt.Fprintf(b, `        <figcaption><span class="mark">%s</span> <span class="n">%d</span> <span class="sum">%s</span> <span class="dur">%s</span>`,
			mark, i+1, html.EscapeString(s.Summary), s.Duration.Round(time.Millisecond))
		if s.Error != "" {
			fmt.Fprintf(b, ` <span class="err">%s</span>`, html.EscapeString(s.Error))
		}
		b.WriteString("</figcaption>\n      </figure>\n")
	}
	b.WriteString("    </div>\n  </div>\n</details>\n")
}

// writeHeal renders the heal diagnostics block (or nothing).
func writeHeal(b *strings.Builder, h *HealInfo) {
	if h == nil {
		return
	}
	cause := "failure"
	if h.TriggerWasTimeout {
		cause = "transient timeout"
	}
	changed := "no"
	if h.StepsChanged {
		changed = fmt.Sprintf("yes (%d&rarr;%d steps)", h.CachedStepCount, h.HealedStepCount)
	}
	turns := fmt.Sprintf("%d turns, %d tool calls", h.AgentTurns, h.AgentToolCalls)
	if h.AgentHitMaxTurns {
		turns += " (hit max turns!)"
	}
	fmt.Fprintf(b, `      <div class="heal">
        <div class="heal-row"><span class="heal-label">Healed</span> triggered by %s on <code>%s</code></div>
        <div class="heal-row"><span class="heal-label">Trigger error</span> <code>%s</code></div>
        <div class="heal-row"><span class="heal-label">Retries before heal</span> %d &middot; <span class="heal-label">agent</span> %s &middot; <span class="heal-label">steps changed</span> %s</div>
      </div>`+"\n",
		cause, html.EscapeString(h.TriggerStepSummary), html.EscapeString(strings.TrimSpace(h.TriggerError)), h.RetriesBeforeHeal, html.EscapeString(turns), changed)
}

// relPath returns p relative to dir, falling back to p on error.
func relPath(dir, p string) string {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return p
	}
	return rel
}

func commaInt(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	return string(out)
}

const reportHeader = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>lazycue report</title>
<style>
  :root { color-scheme: dark; --bg:#0d1117; --panel:#161b22; --line:#30363d; --muted:#8b949e; --text:#e6edf3; --green:#3fb950; --red:#f85149; }
  * { box-sizing: border-box; }
  body { font-family: ui-sans-serif, system-ui, -apple-system, sans-serif; margin: 0; background: var(--bg); color: var(--text); }
  h1 { font-size: 1.25rem; margin: 0; }
  a { color: inherit; }

  .summary { position: sticky; top: 0; z-index: 10; background: rgba(13,17,23,.92); backdrop-filter: blur(8px); border-bottom: 1px solid var(--line); padding: .75rem 1rem; display: flex; flex-wrap: wrap; align-items: center; gap: .6rem 1rem; }
  .stats { display: flex; flex-wrap: wrap; gap: .4rem; }
  .stat { background: var(--panel); border: 1px solid var(--line); border-radius: 999px; padding: .15rem .6rem; font-size: .78rem; }
  .stat.pass { color: var(--green); }
  .stat.fail { color: var(--red); }
  .toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: .4rem; margin-left: auto; }
  .toolbar input[type=search] { background: var(--panel); border: 1px solid var(--line); border-radius: 6px; color: var(--text); padding: .3rem .55rem; font-size: .82rem; min-width: 12rem; }
  .toolbar button { background: var(--panel); border: 1px solid var(--line); border-radius: 6px; color: var(--text); padding: .3rem .6rem; font-size: .82rem; cursor: pointer; }
  .toolbar button:hover { border-color: var(--muted); }
  .toolbar .toggle { font-size: .82rem; color: var(--muted); display: inline-flex; align-items: center; gap: .3rem; cursor: pointer; }

  main { padding: 1rem; display: flex; flex-direction: column; gap: .6rem; }
  .test { border: 1px solid var(--line); border-radius: 8px; overflow: hidden; background: #0f141b; }
  .test.fail { border-color: var(--red); }
  .test[hidden] { display: none; }

  summary { display: flex; align-items: center; gap: .6rem; padding: .55rem .75rem; cursor: pointer; list-style: none; background: var(--panel); }
  summary::-webkit-details-marker { display: none; }
  summary::before { content: "▶"; color: var(--muted); font-size: .7rem; transition: transform .15s; }
  details[open] > summary::before { transform: rotate(90deg); }
  summary:hover { background: #1c232c; }
  .badge { font-weight: 700; font-size: .72rem; padding: .1rem .45rem; border-radius: 4px; flex: none; }
  .badge.pass { background: #1a7f37; color: #fff; }
  .badge.fail { background: #b62324; color: #fff; }
  .sumtext { flex: 1 1 auto; font-size: .9rem; color: #c9d1d9; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tname { flex: none; font-family: ui-monospace, monospace; font-size: .78rem; color: #d2a8ff; background: #1c2333; border: 1px solid #3d4f7a; border-radius: 4px; padding: .05rem .35rem; }
  details[open] > summary .sumtext { white-space: normal; }
  .meta { flex: none; font-size: .76rem; color: var(--muted); }

  .body { display: flex; gap: 1rem; padding: .85rem; align-items: flex-start; }
  .left { flex: 0 0 300px; position: sticky; top: 4.5rem; }
  .left video { width: 100%; border: 1px solid var(--line); border-radius: 6px; background: #000; display: block; }
  .no-video { color: var(--muted); font-size: .85rem; padding: 2rem 0; text-align: center; border: 1px dashed var(--line); border-radius: 6px; }

  .shots { flex: 1 1 auto; display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: .6rem; min-width: 0; }
  figure.shot { margin: 0; border: 1px solid #21262d; border-radius: 6px; overflow: hidden; background: #0d1117; display: flex; flex-direction: column; }
  figure.shot.bad { border-color: var(--red); }
  .shot img { display: block; width: 100%; height: auto; border-bottom: 1px solid var(--line); }
  figcaption { padding: .35rem .45rem; font-size: .74rem; line-height: 1.35; display: flex; flex-wrap: wrap; gap: .25rem .35rem; align-items: baseline; }
  figcaption .n { color: var(--muted); }
  .mark { font-weight: 700; }
  .ok .mark { color: var(--green); }
  .bad .mark { color: var(--red); }
  .sum { font-family: ui-monospace, monospace; word-break: break-word; }
  .dur { color: var(--muted); margin-left: auto; }
  .err { color: #ffa198; font-family: ui-monospace, monospace; flex-basis: 100%; }

  .error { margin-top: .5rem; color: #ffa198; font-family: ui-monospace, monospace; font-size: .78rem; white-space: pre-wrap; }
  .heal { margin-top: .5rem; padding: .5rem .6rem; background: #1c2333; border: 1px solid #3d4f7a; border-left: 3px solid #d29922; border-radius: 6px; font-size: .78rem; }
  .heal-row { margin: .15rem 0; color: #c9d1d9; }
  .heal-label { color: var(--muted); font-weight: 600; }
  .heal code { font-family: ui-monospace, monospace; color: #d2a8ff; word-break: break-all; }

  @media (max-width: 720px) {
    .body { flex-direction: column; }
    .left { position: static; flex-basis: auto; width: 100%; max-width: 320px; }
  }
</style>
</head>
<body>
`

const reportFooter = `<script>
(function () {
  var tests = Array.prototype.slice.call(document.querySelectorAll('details.test'));
  function setAll(open) { tests.forEach(function (d) { d.open = open; }); }
  document.querySelectorAll('.toolbar [data-act]').forEach(function (btn) {
    btn.addEventListener('click', function () { setAll(btn.dataset.act === 'expand'); });
  });
  var filter = document.getElementById('filter');
  var onlyFail = document.getElementById('only-fail');
  function apply() {
    var q = (filter.value || '').trim().toLowerCase();
    var failOnly = onlyFail.checked;
    tests.forEach(function (d) {
      var matchText = !q || (d.dataset.search || '').indexOf(q) !== -1;
      var matchFail = !failOnly || d.classList.contains('fail');
      var show = matchText && matchFail;
      d.hidden = !show;
      if (show && q) d.open = true;
    });
  }
  filter.addEventListener('input', apply);
  onlyFail.addEventListener('change', apply);
})();
</script>
</body>
</html>
`
