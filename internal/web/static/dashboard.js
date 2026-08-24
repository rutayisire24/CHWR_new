// Dashboard charts. Chart.js is vendored under /static/vendor — there is no
// build step and no CDN, and the content security policy allows neither.
//
// The figures arrive in a `type="application/json"` block the server rendered:
// a data block, not a script, so the policy that forbids inline script is not
// bent to draw a chart. Every number plotted here also appears on the page as
// text or inside the card's own table, so nothing is reachable only by hover.
(function () {
  'use strict';

  var node = document.getElementById('dashboard-data');
  if (!node || typeof Chart === 'undefined') return; // charts are an enhancement
  var d;
  try {
    d = JSON.parse(node.textContent);
  } catch (e) {
    return;
  }

  // The palette lives in app.css, by role. Reading it back here keeps one
  // source for the colours rather than two that drift.
  var css = getComputedStyle(document.documentElement);
  function token(name) { return css.getPropertyValue(name).trim(); }

  var C = {
    s1: token('--viz-1'),
    s2: token('--viz-2'),
    part: token('--viz-part'),
    dim: token('--viz-dim'),
    grid: token('--viz-grid'),
    axis: token('--viz-axis'),
    ink: token('--ink'),
    soft: token('--ink-soft'),
    surface: token('--paper') || '#ffffff'
  };

  var SANS = 'system-ui, -apple-system, "Segoe UI", Roboto, sans-serif';

  Chart.defaults.font.family = SANS;
  Chart.defaults.font.size = 12;
  Chart.defaults.color = C.soft;
  Chart.defaults.animation.duration = 320;
  Chart.defaults.plugins.legend.display = false; // legends are HTML, in ink
  Chart.defaults.plugins.tooltip.backgroundColor = C.ink;
  Chart.defaults.plugins.tooltip.padding = 10;
  Chart.defaults.plugins.tooltip.cornerRadius = 6;
  Chart.defaults.plugins.tooltip.titleFont = { family: SANS, size: 12, weight: '600' };
  Chart.defaults.plugins.tooltip.bodyFont = { family: SANS, size: 12 };
  Chart.defaults.plugins.tooltip.boxPadding = 4;

  var nf = new Intl.NumberFormat('en');
  function n(v) { return nf.format(v); }

  // The value axis, wherever it points: a hairline solid grid one step off the
  // surface, ticks rounded to clean thousands. Never dashed — a dashed rule
  // reads as a threshold when it is only a grid.
  function valueAxis(extra) {
    var a = {
      beginAtZero: true,
      border: { display: false },
      grid: { color: C.grid, drawTicks: false, lineWidth: 1 },
      // Counts are whole people: without this a register holding one record
      // per domain draws an axis of tenths.
      ticks: { padding: 8, precision: 0, callback: function (v) { return n(v); } }
    };
    return Object.assign(a, extra || {});
  }

  // The category axis carries names, not magnitude: no grid, one hairline rule.
  function categoryAxis(extra) {
    var a = {
      border: { color: C.axis },
      grid: { display: false },
      ticks: { padding: 6, autoSkip: false, color: C.soft }
    };
    return Object.assign(a, extra || {});
  }

  // Bars are thin, with a 4px rounded data-end and a square foot at the
  // baseline; the leftover band width is deliberate air.
  var BAR = { maxBarThickness: 22, categoryPercentage: 0.84, barPercentage: 0.9 };

  // directLabels writes the value at the tip of a single-series bar or the cap
  // of a column — selectively, never a number on every point of every chart.
  // It measures first and skips any label that will not fit, because a clipped
  // label is worse than an axis tick.
  var directLabels = {
    id: 'directLabels',
    afterDatasetsDraw: function (chart, args, opts) {
      var cfg = opts || {};
      if (!cfg.on) return;
      var ctx = chart.ctx;
      var meta = chart.getDatasetMeta(cfg.dataset || 0);
      var horizontal = chart.options.indexAxis === 'y';
      ctx.save();
      ctx.font = '600 11px ' + SANS;
      ctx.fillStyle = C.soft;
      ctx.textBaseline = horizontal ? 'middle' : 'bottom';
      ctx.textAlign = horizontal ? 'left' : 'center';
      meta.data.forEach(function (bar, i) {
        var raw = chart.data.datasets[cfg.dataset || 0].data[i];
        if (raw === null || raw === undefined) return; // a measured zero is a
        // finding and keeps its label; a missing value has nothing to say
        var text = cfg.format ? cfg.format(raw, i) : n(raw);
        // Measured against the canvas rather than the plot area: the card
        // reserves padding outside the scale precisely so a label at the tip
        // of a full-length bar has somewhere to sit.
        if (horizontal) {
          if (bar.x + 6 + ctx.measureText(text).width > chart.width - 2) return;
          ctx.fillText(text, bar.x + 6, bar.y);
        } else {
          if (bar.y - 6 < 10) return;
          ctx.fillText(text, bar.x, bar.y - 6);
        }
      });
      ctx.restore();
    }
  };

  // insideLabels writes a share inside a stacked segment, and only when the
  // text fits with padding on both sides. The colour is picked against the
  // fill, which is the one place a label may leave the ink tokens.
  var insideLabels = {
    id: 'insideLabels',
    afterDatasetsDraw: function (chart, args, opts) {
      if (!opts || !opts.on) return;
      var ctx = chart.ctx;
      ctx.save();
      ctx.font = '600 11px ' + SANS;
      ctx.textAlign = 'center';
      ctx.textBaseline = 'middle';
      chart.data.datasets.forEach(function (ds, di) {
        var meta = chart.getDatasetMeta(di);
        if (meta.hidden) return;
        meta.data.forEach(function (seg, i) {
          var v = ds.data[i];
          if (!v) return;
          var text = opts.format ? opts.format(v, i, di) : n(v);
          var w = seg.width !== undefined ? seg.width : 0;
          if (ctx.measureText(text).width + 16 > w) return; // would clip: leave it to the table
          ctx.fillStyle = opts.light && opts.light.indexOf(di) >= 0 ? C.ink : '#ffffff';
          ctx.fillText(text, (seg.x + seg.base) / 2, seg.y);
        });
      });
      ctx.restore();
    }
  };

  function mount(id) {
    var el = document.getElementById(id);
    return el ? el.getContext('2d') : null;
  }

  // --- CHWs by area ------------------------------------------------------
  // Nominal categories ranked by size: one series, one colour. A ramp here
  // would encode bar length twice and say nothing new.
  var areas = mount('chart-areas');
  if (areas && d.areas.labels.length) {
    new Chart(areas, {
      type: 'bar',
      data: {
        labels: d.areas.labels,
        datasets: [Object.assign({
          data: d.areas.values,
          backgroundColor: C.s1,
          borderRadius: 4,
          borderSkipped: 'start',
          hoverBackgroundColor: C.s1
        }, BAR)]
      },
      options: {
        indexAxis: 'y',
        maintainAspectRatio: false,
        layout: { padding: { right: 44 } }, // room for the tip labels
        onClick: function (evt, hit) {
          var href = hit.length && d.areaHrefs[hit[0].index];
          if (href) window.location.href = href;
        },
        onHover: function (evt, hit) {
          evt.native.target.style.cursor =
            hit.length && d.areaHrefs[hit[0].index] ? 'pointer' : 'default';
        },
        interaction: { mode: 'nearest', axis: 'y', intersect: false },
        scales: { x: valueAxis({ display: false }), y: categoryAxis() },
        plugins: {
          directLabels: { on: true },
          tooltip: {
            callbacks: {
              label: function (c) {
                var share = d.total ? ' · ' + (c.parsed.x / d.total * 100).toFixed(1) + '% of the register' : '';
                return n(c.parsed.x) + ' CHWs' + share;
              }
            }
          }
        }
      },
      plugins: [directLabels]
    });
  }

  // --- Age distribution --------------------------------------------------
  // Ordered bands, one series: the shape is the point, so nothing is labelled
  // except the axis and the tooltip.
  var ages = mount('chart-ages');
  if (ages && d.ages.labels.length) {
    new Chart(ages, {
      type: 'bar',
      data: {
        labels: d.ages.labels,
        datasets: [Object.assign({
          data: d.ages.values,
          backgroundColor: C.s1,
          borderRadius: 4,
          borderSkipped: 'start',
          hoverBackgroundColor: C.s1
        }, BAR)]
      },
      options: {
        maintainAspectRatio: false,
        interaction: { mode: 'index', intersect: false },
        scales: { y: valueAxis(), x: categoryAxis({ ticks: { padding: 6, autoSkip: false, maxRotation: 0, color: C.soft } }) },
        plugins: {
          tooltip: {
            callbacks: {
              title: function (c) { return c[0].label + ' years'; },
              label: function (c) { return n(c.parsed.y) + ' CHWs'; }
            }
          }
        }
      }
    });
  }

  // --- Composition, by cadre --------------------------------------------
  // Hundred-percent stacks, because the question is the mix and not the size:
  // 347 CHEWs beside 24,226 VHTs on one absolute scale is a bar and a sliver.
  // The counts stay in the card's table.
  function composition(id, parts) {
    var el = mount(id);
    if (!el || !d.cadres.length) return;
    var totals = d.cadres.map(function (_, i) {
      return parts.reduce(function (sum, p) { return sum + (p.data[i] || 0); }, 0);
    });
    new Chart(el, {
      type: 'bar',
      data: {
        labels: d.cadres,
        datasets: parts.map(function (p) {
          return Object.assign({
            label: p.label,
            data: p.data.map(function (v, i) { return totals[i] ? v / totals[i] * 100 : 0; }),
            backgroundColor: p.color,
            // A 2px border in the surface colour is the gap between segments —
            // white doing the separating, rather than a stroke round a mark.
            borderColor: C.surface,
            borderWidth: { right: 2 },
            borderSkipped: false
          }, BAR);
        })
      },
      options: {
        indexAxis: 'y',
        maintainAspectRatio: false,
        interaction: { mode: 'nearest', axis: 'y', intersect: false },
        scales: {
          x: { stacked: true, max: 100, display: false },
          y: Object.assign(categoryAxis(), { stacked: true })
        },
        plugins: {
          insideLabels: { on: true, format: function (v) { return Math.round(v) + '%'; } },
          tooltip: {
            callbacks: {
              label: function (c) {
                var abs = parts[c.datasetIndex].data[c.dataIndex];
                return c.dataset.label + ': ' + n(abs) + ' (' + c.parsed.x.toFixed(1) + '%)';
              }
            }
          }
        }
      },
      plugins: [insideLabels]
    });
  }

  composition('chart-sex', [
    { label: 'Female', data: d.female, color: C.s1 },
    { label: 'Male', data: d.male, color: C.s2 }
  ]);
  // Status is emphasis, not identity: one hue for the state that matters and
  // the de-emphasis grey for its complement. Green would say "good" here, and
  // on this site green already means exactly one thing.
  composition('chart-status', [
    { label: 'Active', data: d.active, color: C.s1 },
    { label: 'Inactive', data: d.inactive, color: C.dim }
  ]);

  // --- Record completeness ----------------------------------------------
  var fields = mount('chart-fields');
  if (fields && d.fields.labels.length) {
    new Chart(fields, {
      type: 'bar',
      data: {
        labels: d.fields.labels,
        datasets: [Object.assign({
          data: d.fieldPct,
          backgroundColor: C.s1,
          borderRadius: 4,
          borderSkipped: 'start',
          hoverBackgroundColor: C.s1
        }, BAR)]
      },
      options: {
        indexAxis: 'y',
        maintainAspectRatio: false,
        layout: { padding: { right: 40 } },
        interaction: { mode: 'nearest', axis: 'y', intersect: false },
        scales: {
          x: valueAxis({ max: 100, ticks: { padding: 8, callback: function (v) { return v + '%'; } } }),
          y: categoryAxis()
        },
        plugins: {
          directLabels: { on: true, format: function (v) { return v + '%'; } },
          tooltip: {
            callbacks: {
              label: function (c) {
                return n(d.fields.values[c.dataIndex]) + ' of ' + n(d.total) + ' records (' + c.parsed.x + '%)';
              }
            }
          }
        }
      },
      plugins: [directLabels]
    });
  }

  // --- Service domains ---------------------------------------------------
  // A whole and its part: `trained_implies_provides` makes recently-trained a
  // subset of offered, so the two are stacked to the total rather than drawn
  // side by side, and the part takes a lighter step of the same hue. A second
  // hue would claim they are different things.
  var services = mount('chart-services');
  if (services && d.services.labels.length) {
    new Chart(services, {
      type: 'bar',
      data: {
        labels: d.services.labels,
        datasets: [
          Object.assign({ label: 'Trained in the last 2 years', data: d.trained, backgroundColor: C.s1 }, BAR),
          Object.assign({ label: 'Offered, not recently trained', data: d.services.values, backgroundColor: C.part, borderRadius: { topRight: 4, bottomRight: 4 } }, BAR)
        ].map(function (ds) {
          return Object.assign(ds, { borderColor: C.surface, borderWidth: { right: 2 }, borderSkipped: false });
        })
      },
      options: {
        indexAxis: 'y',
        maintainAspectRatio: false,
        interaction: { mode: 'nearest', axis: 'y', intersect: false },
        scales: {
          x: Object.assign(valueAxis(), { stacked: true }),
          y: Object.assign(categoryAxis(), { stacked: true })
        },
        plugins: {
          tooltip: {
            callbacks: {
              label: function (c) { return c.dataset.label + ': ' + n(c.parsed.x); },
              footer: function (items) {
                var i = items[0].dataIndex;
                return 'Offers the service: ' + n(d.trained[i] + d.services.values[i]);
              }
            }
          }
        }
      }
    });
  }
})();
