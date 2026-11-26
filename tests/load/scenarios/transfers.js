// Concurrent transfers.
//
// Two scenarios run at once:
//   senders    - distinct users, each sending from its own wallet
//   contention - every virtual user sends from the same hot wallet, which forces
//                concurrent row locks on one wallet and measures what that costs
//
// Both use unique idempotency keys, matching currencies, and funded wallets, so
// a 4xx means something is wrong with the fixture rather than with the system.

import { check, sleep } from 'k6';
import exec from 'k6/execution';
import { Counter, Rate } from 'k6/metrics';
import { config, banner } from '../lib/env.js';
import { postJSON, errorCode, guardRateLimit } from '../lib/http.js';
import { uuid } from '../lib/id.js';
import { provision } from '../lib/data.js';
import { summary, TREND_STATS } from '../lib/summary.js';

// A transfer that was accepted. This is the metric the success threshold uses,
// so business rejections can never be counted as success.
const created = new Rate('transfer_created');
// Expected business answers, kept apart from unexpected server errors.
const businessRejected = new Counter('transfer_business_rejected');
const serverErrors = new Counter('transfer_server_errors');

// POST /api/v1/transfers answers 201 when the transfer committed, and these
// business failures instead. Any of them here means the fixture is wrong:
//   400 invalid_amount | invalid_currency | invalid_transfer | invalid_wallet_id |
//       invalid_idempotency_key | currency_mismatch
//   404 wallet_not_found          - source wallet not owned, or wallet missing
//   409 idempotency_conflict      - key reused with a different payload
//   422 insufficient_funds        - the sender ran out of money
//   422 balance_limit             - the destination would overflow int64
function transfer(account, receiver, tag) {
  const response = postJSON(
    '/api/v1/transfers',
    {
      source_wallet_id: account.walletId,
      destination_wallet_id: receiver.walletId,
      amount: config.funds.transferAmount,
      currency: config.funds.currency,
    },
    account.token,
    tag,
    { 'Idempotency-Key': uuid() }
  );
  guardRateLimit(response, 'creating transfers');

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

export function senderFlow(data) {
  const account = data.senders[exec.vu.idInTest % data.senders.length];
  const receiver = data.receivers[exec.vu.idInTest % data.receivers.length];
  transfer(account, receiver, 'transfer_create');

  if (config.transfers.pause > 0) {
    sleep(config.transfers.pause);
  }
}

export function contentionFlow(data) {
  const receiver = data.receivers[exec.vu.idInTest % data.receivers.length];
  transfer(data.hot, receiver, 'transfer_create_hot');

  if (config.transfers.pause > 0) {
    sleep(config.transfers.pause);
  }
}

function scenarios() {
  const result = {
    senders: {
      executor: 'constant-vus',
      vus: config.transfers.vus,
      duration: config.transfers.duration,
      exec: 'senderFlow',
      tags: { flow: 'senders' },
      gracefulStop: '10s',
    },
  };
  // k6 rejects a constant-vus scenario with zero virtual users, so contention is
  // only registered when it was asked for.
  if (config.topology.contentionVus > 0) {
    result.contention = {
      executor: 'constant-vus',
      vus: config.topology.contentionVus,
      duration: config.transfers.duration,
      exec: 'contentionFlow',
      tags: { flow: 'contention' },
      gracefulStop: '10s',
    };
  }
  return result;
}

export const options = {
  scenarios: scenarios(),
  thresholds: {
    'http_req_failed{flow:senders}': ['rate<' + config.thresholds.httpFailedRate],
    'http_req_failed{flow:contention}': ['rate<' + config.thresholds.httpFailedRate],
    'http_req_duration{name:transfer_create}': ['p(95)<' + config.thresholds.p95Ms],
    'http_req_duration{name:transfer_create_hot}': [
      'p(95)<' + config.thresholds.contentionP95Ms,
    ],
    'http_req_duration': ['p(99)<' + config.thresholds.p99Ms],
    transfer_created: ['rate>' + config.thresholds.transferSuccessRate],
    transfer_business_rejected: ['count<=' + config.thresholds.businessRejected],
    transfer_server_errors: ['count==0'],
  },
  summaryTrendStats: TREND_STATS,
};

export function setup() {
  console.log(banner('transfers'));
  return provision();
}

export function handleSummary(data) {
  return summary(
    'transfers',
    {
      senders: { vus: config.transfers.vus, duration: config.transfers.duration },
      contention: {
        vus: config.topology.contentionVus,
        duration: config.transfers.duration,
      },
      amount: config.funds.transferAmount,
      currency: config.funds.currency,
      pause_seconds: config.transfers.pause,
    },
    data
  );
}
