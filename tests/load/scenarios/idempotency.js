// Idempotency: the same request repeated with the same key.
//
// This is a correctness check, not a throughput measurement, so it runs one
// virtual user for one iteration and asserts on every observation. The
// threshold is checks == 1: a single failed assertion fails the run.
//
// The point is that retrying an operation whose outcome the client never saw
// must not move money twice.

import http from 'k6/http';
import { check } from 'k6';
import { config, banner } from '../lib/env.js';
import { getJSON, postJSON, body, errorCode, guardRateLimit } from '../lib/http.js';
import { uuid } from '../lib/id.js';
import { provision } from '../lib/data.js';
import { summary, TREND_STATS } from '../lib/summary.js';

function balanceOf(account) {
  const response = getJSON('/api/v1/wallets/' + account.walletId, account.token, 'wallet_get');
  const parsed = body(response);
  if (response.status !== 200 || !parsed) {
    throw new Error('reading the balance failed with ' + response.status + ' ' + errorCode(response));
  }
  return parsed.balance;
}

function sendTransfer(account, receiver, amount, key, tag, responseCallback) {
  const response = postJSON(
    '/api/v1/transfers',
    {
      source_wallet_id: account.walletId,
      destination_wallet_id: receiver.walletId,
      amount: amount,
      currency: config.funds.currency,
    },
    account.token,
    tag,
    { 'Idempotency-Key': key },
    responseCallback
  );
  guardRateLimit(response, 'replaying a transfer');
  return response;
}

export const options = {
  scenarios: {
    idempotency: {
      executor: 'per-vu-iterations',
      vus: 1,
      iterations: 1,
      gracefulStop: '10s',
    },
  },
  thresholds: {
    checks: ['rate==1'],
    http_req_failed: ['rate<' + config.thresholds.httpFailedRate],
  },
  summaryTrendStats: TREND_STATS,
};

export function setup() {
  console.log(banner('idempotency'));
  return provision();
}

export default function (data) {
  const account = data.senders[0];
  const receiver = data.receivers[0];
  const amount = config.funds.transferAmount;
  const repeats = config.idempotency.repeats;

  const before = balanceOf(account);
  const key = uuid();

  // Every attempt uses the same key and the same payload, so the API must treat
  // them as one logical operation.
  const responses = [];
  for (let attempt = 0; attempt < repeats; attempt++) {
    responses.push(sendTransfer(account, receiver, amount, key, 'transfer_replay'));
  }

  const after = balanceOf(account);

  const statuses = [];
  const transferIds = [];
  let replays = 0;
  let originals = 0;
  for (let i = 0; i < responses.length; i++) {
    statuses.push(responses[i].status);
    const parsed = body(responses[i]);
    transferIds.push(parsed && parsed.transfer_id ? parsed.transfer_id : '');
    if (responses[i].headers['Idempotent-Replay'] === 'true') {
      replays++;
    } else {
      originals++;
    }
  }

  let identicalIds = transferIds.length > 0 && transferIds[0] !== '';
  for (let i = 1; i < transferIds.length; i++) {
    if (transferIds[i] !== transferIds[0]) {
      identicalIds = false;
    }
  }

  check(statuses, {
    'every attempt returned 201': function (observed) {
      for (let i = 0; i < observed.length; i++) {
        if (observed[i] !== 201) {
          return false;
        }
      }
      return true;
    },
  });

  check(identicalIds, {
    'every attempt reports the same transfer id': function (observed) {
      return observed;
    },
  });

  check(originals, {
    'exactly one attempt created the transfer': function (observed) {
      return observed === 1;
    },
  });

  check(replays, {
    'every other attempt was marked as a replay': function (observed) {
      return observed === repeats - 1;
    },
  });

  check(before - after, {
    'the balance moved exactly once': function (observed) {
      return observed === amount;
    },
  });

  // The same key with a different payload is a different operation, and the API
  // must refuse it rather than replay the first one. 409 is the expected answer
  // here, so it must not count as a failed request.
  const conflict = sendTransfer(
    account,
    receiver,
    amount + 1,
    key,
    'transfer_replay_conflict',
    http.expectedStatuses(201, 409)
  );
  check(conflict, {
    'reusing the key with a different amount is rejected': function (response) {
      return response.status === 409 && errorCode(response) === 'idempotency_conflict';
    },
  });

  check(balanceOf(account), {
    'the rejected attempt changed nothing': function (observed) {
      return observed === after;
    },
  });
}

export function handleSummary(data) {
  return summary(
    'idempotency',
    {
      repeats: config.idempotency.repeats,
      amount: config.funds.transferAmount,
      currency: config.funds.currency,
      writes: true,
    },
    data
  );
}
