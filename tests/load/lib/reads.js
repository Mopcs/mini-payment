// The read path, shared by the baseline and stress scenarios.
//
// One iteration touches every endpoint a client uses outside of a payment:
// liveness, readiness, the wallet list, a single wallet, and the transfer
// history. Each request carries its own tag so a slow endpoint is identifiable
// in the results.

import { check } from 'k6';
import { getJSON, body } from './http.js';

export function readFlow(account) {
  const health = getJSON('/health', null, 'health');
  check(health, {
    'health returns 200': function (response) {
      return response.status === 200;
    },
  });

  const ready = getJSON('/ready', null, 'ready');
  check(ready, {
    'readiness returns 200': function (response) {
      return response.status === 200;
    },
  });

  const wallets = getJSON('/api/v1/wallets', account.token, 'wallets_list');
  check(wallets, {
    'wallet list returns at least one wallet': function (response) {
      const parsed = body(response);
      return response.status === 200 && parsed && parsed.wallets && parsed.wallets.length > 0;
    },
  });

  const wallet = getJSON('/api/v1/wallets/' + account.walletId, account.token, 'wallet_get');
  check(wallet, {
    'wallet read returns the requested wallet': function (response) {
      const parsed = body(response);
      return response.status === 200 && parsed && parsed.id === account.walletId;
    },
  });

  const history = getJSON('/api/v1/transfers?limit=20', account.token, 'transfer_history');
  check(history, {
    'transfer history returns 200': function (response) {
      const parsed = body(response);
      return response.status === 200 && parsed && parsed.items !== undefined;
    },
  });
}
