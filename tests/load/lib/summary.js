// Result reporting shared by every scenario.
//
// Each scenario exports handleSummary, which k6 calls once at the end. The
// report keeps what is needed to compare two runs: the scenario, the plan it
// ran, the target, the threshold verdicts, and every metric k6 collected.
//
// k6 does not export percentiles that no threshold asks for, so every scenario
// sets summaryTrendStats to the list below.

import { config } from './env.js';

export const TREND_STATS = ['avg', 'min', 'med', 'max', 'p(50)', 'p(90)', 'p(95)', 'p(99)'];

function stamp() {
  return new Date().toISOString().replace(/[:.]/g, '-');
}

function pad(label, width) {
  let text = label;
  while (text.length < width) {
    text = text + ' ';
  }
  return text;
}

function metricValue(data, name, key) {
  const metric = data.metrics[name];
  if (!metric || !metric.values) {
    return null;
  }
  const value = metric.values[key];
  return typeof value === 'number' ? value : null;
}

function milliseconds(value) {
  return value === null ? 'n/a' : value.toFixed(0) + 'ms';
}

function percentage(value) {
  return value === null ? 'n/a' : (value * 100).toFixed(2) + '%';
}

// k6 attaches the verdicts to each metric rather than to a top-level map, so
// they are collected by walking the metrics.
function thresholdVerdicts(metrics) {
  const passed = [];
  const failed = [];
  Object.keys(metrics).forEach(function (name) {
    const thresholds = metrics[name].thresholds;
    if (!thresholds) {
      return;
    }
    Object.keys(thresholds).forEach(function (expression) {
      const label = name + ': ' + expression;
      if (thresholds[expression].ok) {
        passed.push(label);
      } else {
        failed.push(label);
      }
    });
  });
  return { passed: passed, failed: failed };
}

export function summary(scenario, plan, data) {
  const verdicts = thresholdVerdicts(data.metrics);
  const requests = metricValue(data, 'http_reqs', 'count');
  const throughput = metricValue(data, 'http_reqs', 'rate');
  const failedRate = metricValue(data, 'http_req_failed', 'rate');
  const p50 = metricValue(data, 'http_req_duration', 'p(50)');
  const p95 = metricValue(data, 'http_req_duration', 'p(95)');
  const p99 = metricValue(data, 'http_req_duration', 'p(99)');
  const checks = metricValue(data, 'checks', 'rate');

  const path = config.output.dir + '/' + scenario + '-' + stamp() + '.json';
  const report = {
    scenario: scenario,
    generated_at: new Date().toISOString(),
    target: config.target.baseUrl,
    command: config.output.command,
    plan: plan,
    thresholds: config.thresholds,
    threshold_verdicts: verdicts,
    headline: {
      requests: requests,
      requests_per_second: throughput,
      http_failed_rate: failedRate,
      duration_ms: { p50: p50, p95: p95, p99: p99 },
      checks_rate: checks,
    },
    metrics: data.metrics,
  };

  const lines = [
    '',
    '=== ' + scenario + ' ===',
    pad('target', 14) + config.target.baseUrl,
    pad('command', 14) + config.output.command,
    pad('requests', 14) + (requests === null ? 'n/a' : requests) +
      (throughput === null ? '' : ' (' + throughput.toFixed(1) + '/s)'),
    pad('failed', 14) + percentage(failedRate),
    pad('duration', 14) + 'p50 ' + milliseconds(p50) + '  p95 ' + milliseconds(p95) +
      '  p99 ' + milliseconds(p99),
    pad('checks', 14) + percentage(checks),
    pad('thresholds', 14) + verdicts.passed.length + ' passed, ' + verdicts.failed.length + ' failed',
  ];
  if (verdicts.failed.length > 0) {
    lines.push(pad('', 14) + 'failed: ' + verdicts.failed.join(', '));
  }
  lines.push(pad('report', 14) + path);

  const result = { stdout: lines.join('\n') + '\n' };
  result[path] = JSON.stringify(report, null, 2);
  return result;
}
