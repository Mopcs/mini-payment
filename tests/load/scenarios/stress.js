// Stress: staged virtual users, from warm-up to recovery.
//
// The stage plan is configuration, not a promise. The defaults are a shape that
// usually finds a knee on a laptop, not a claim about capacity. Each stage ramps
// to its target over its duration, so the run climbs instead of jumping.
//
// The mix is deliberately mostly writes: a stress test that only reads measures
// the cheapest path in the system.

import { check, sleep } from 'k6';
import exec from 'k6/execution';
import { Counter, Rate } from 'k6/metrics';
import { config, banner } from '../lib/env.js';
import { readFlow } from '../lib/reads.js';
import { postJSON, errorCode, guardRateLimit } from '../lib/http.js';
import { uuid } from '../lib/id.js';
import { provision } from '../lib/data.js';
import { summary, TREND_STATS } from '../lib/summary.js';

const created = new Rate('transfer_created');
const businessRejected = new Counter('transfer_business_rejected');
const serverErrors = new Counter('transfer_server_errors');

function transfer(account, receiver) {
  const response = postJSON(
    '/api/v1/transfers',
    {
      source_wallet_id: account.walletId,
      destination_wallet_id: receiver.walletId,
      amount: config.funds.transferAmount,
      currency: config.funds.currency,
    },
    account.token,
    'transfer_create',
    { 'Idempotency-Key': uuid() }
  );
  guardRateLimit(response, 'creating transfers under stress');

  const accepted = response.status === 201;
  created.add(accepted);

  if (response.status >= 500) {
    serverErrors.add(1);
  } else if (!accepted) {
    businessRejected.add(1, { code: errorCode(response) });
  }

  check(response, {
    'transfer committed': function (result) {
      return result.status === 201;
    },
  });
}

function stages() {
  return config.stress.stages.map(function (stage) {
    return { duration: stage.duration, target: stage.target };
  });
}

export const options = {
  scenarios: {
    stress: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: stages(),
      gracefulRampDown: '10s',
      tags: { flow: 'stress' },
    },
  },
  // Latency uses the stress budgets: this run is expected to saturate. Error and
  // success rates keep the normal budgets, because they must not degrade.
  thresholds: {
    'http_req_failed{flow:stress}': ['rate<' + config.thresholds.httpFailedRate],
    'http_req_duration{name:transfer_create}': [
      'p(95)<' + config.thresholds.stressP95Ms,
    ],
    'http_req_duration': ['p(99)<' + config.thresholds.stressP99Ms],
    transfer_created: ['rate>' + config.thresholds.transferSuccessRate],
    transfer_business_rejected: ['count<=' + config.thresholds.businessRejected],
    transfer_server_errors: ['count==0'],
  },
  summaryTrendStats: TREND_STATS,
};

export function setup() {
  console.log(banner('stress'));
  const plan = config.stress.stages
    .map(function (stage) {
      return stage.label + ' -> ' + stage.target + ' VUs over ' + stage.duration;
    })
    .join(', ');
  console.log('stages: ' + plan);
  return provision();
}

export default function (data) {
  const account = data.senders[exec.vu.idInTest % data.senders.length];

  if (Math.random() < config.stress.readShare) {
    readFlow(account);
  } else {
    const receiver = data.receivers[exec.vu.idInTest % data.receivers.length];
    transfer(account, receiver);
  }

  if (config.stress.pause > 0) {
    sleep(config.stress.pause);
  }
}

export function handleSummary(data) {
  return summary(
    'stress',
    {
      stages: config.stress.stages,
      read_share: config.stress.readShare,
      amount: config.funds.transferAmount,
      currency: config.funds.currency,
      pause_seconds: config.stress.pause,
    },
    data
  );
}
