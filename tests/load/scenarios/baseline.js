// Baseline: the read path at a small, steady load.
//
// This scenario exists to establish the shape of a healthy run before any
// concurrency is added. It never writes, so it can be repeated without touching
// balances.

import { sleep } from 'k6';
import exec from 'k6/execution';
import { config, banner } from '../lib/env.js';
import { readFlow } from '../lib/reads.js';
import { provision } from '../lib/data.js';
import { summary, TREND_STATS } from '../lib/summary.js';

function thresholds() {
  const result = {};
  result['http_req_failed'] = ['rate<' + config.thresholds.httpFailedRate];
  result['http_req_duration'] = [
    'p(95)<' + config.thresholds.p95Ms,
    'p(99)<' + config.thresholds.p99Ms,
  ];
  result['checks'] = ['rate>0.99'];

  const endpoints = ['health', 'ready', 'wallets_list', 'wallet_get', 'transfer_history'];
  endpoints.forEach(function (name) {
    result['http_req_duration{name:' + name + '}'] = [
      'p(95)<' + config.thresholds.p95Ms,
    ];
  });
  return result;
}

export const options = {
  scenarios: {
    baseline: {
      executor: 'constant-vus',
      vus: config.baseline.vus,
      duration: config.baseline.duration,
      gracefulStop: '5s',
    },
  },
  thresholds: thresholds(),
  summaryTrendStats: TREND_STATS,
};

export function setup() {
  console.log(banner('baseline'));
  return provision();
}

export default function (data) {
  const account = data.senders[exec.vu.idInTest % data.senders.length];
  readFlow(account);

  if (config.baseline.pause > 0) {
    sleep(config.baseline.pause);
  }
}

export function handleSummary(data) {
  return summary(
    'baseline',
    {
      executor: 'constant-vus',
      vus: config.baseline.vus,
      duration: config.baseline.duration,
      pause_seconds: config.baseline.pause,
      writes: false,
    },
    data
  );
}
