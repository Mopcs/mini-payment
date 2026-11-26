// Test data provisioning.
//
// Runs once per test in k6's setup phase, never per iteration. The account set
// is fixed and derived from LOAD_TEST_USER_PREFIX, so a rerun reuses the same
// users and wallets instead of creating new ones.
//
// Setup traffic is tagged setup_* so it does not land in the endpoint metrics
// the scenarios measure.

import { config } from './env.js';
import http from 'k6/http';
import { getJSON, postJSON, body, errorCode, guardRateLimit } from './http.js';
import { registerOrLogin } from './auth.js';

const CURRENCY = config.funds.currency;

function accountEmail(role, index) {
  const suffix = ('0' + index).slice(-2);
  return (
    config.credentials.userPrefix + '-' + role + '-' + suffix + '@' + config.credentials.emailDomain
  );
}

function walletsOf(token) {
  const response = getJSON('/api/v1/wallets', token, 'setup_wallets_list');
  const parsed = body(response);
  if (response.status !== 200 || !parsed || !parsed.wallets) {
    throw new Error('listing wallets failed with ' + response.status + ' ' + errorCode(response));
  }
  return parsed.wallets;
}

function walletOf(token, walletID) {
  const response = getJSON('/api/v1/wallets/' + walletID, token, 'setup_wallet_get');
  if (response.status !== 200) {
    throw new Error(
      'reading wallet ' + walletID + ' failed with ' + response.status + ' ' + errorCode(response)
    );
  }
  return body(response);
}

function findCurrencyWallet(wallets) {
  for (let i = 0; i < wallets.length; i++) {
    if (wallets[i].currency === CURRENCY) {
      return wallets[i].id;
    }
  }
  return '';
}

function ensureWallet(token) {
  const existing = findCurrencyWallet(walletsOf(token));
  if (existing) {
    return existing;
  }

  const created = postJSON('/api/v1/wallets', { currency: CURRENCY }, token, 'setup_wallet_create');
  if (created.status === 201) {
    return body(created).id;
  }
  if (created.status === 409) {
    // Created between the list and the create call.
    const retried = findCurrencyWallet(walletsOf(token));
    if (retried) {
      return retried;
    }
  }
  throw new Error(
    'creating a ' + CURRENCY + ' wallet failed with ' + created.status + ' ' + errorCode(created)
  );
}

// The seed key is derived from the amount, so a rerun with the same amount is a
// replay and does not add funds a second time.
function seedKey(account, amount) {
  return 'k6-seed-' + account.role + '-' + account.index + '-' + amount;
}

function fund(account, target) {
  const current = walletOf(account.token, account.walletId);
  if (current.balance >= target) {
    return;
  }

  const amount = target - current.balance;
  const response = postJSON(
    '/api/v1/wallets/' + account.walletId + '/deposits',
    { amount: amount, currency: CURRENCY },
    account.token,
    'setup_deposit',
    { 'Idempotency-Key': seedKey(account, amount) }
  );
  guardRateLimit(response, 'seeding test funds');

  if (response.status !== 201) {
    throw new Error(
      'seeding ' + account.email + ' failed with ' + response.status + ' ' + errorCode(response)
    );
  }

  const funded = walletOf(account.token, account.walletId);
  if (funded.balance < target) {
    throw new Error(
      'wallet ' + account.walletId + ' holds ' + funded.balance + ' after seeding, expected ' + target
    );
  }
}

function provisionAccount(role, index) {
  const email = accountEmail(role, index);
  const token = registerOrLogin(email);
  return { role: role, index: index, email: email, token: token, walletId: ensureWallet(token) };
}

// Every wallet must be invisible to the other test users. The API answers 404
// for a wallet the caller does not own, so a 200 here means the fixture is
// pointing at the wrong wallet and the whole run would measure nonsense.
//
// The 404 is the expected answer, so it must not count towards the failed
// request budget the scenarios threshold on.
function verifyOwnership(accounts) {
  for (let i = 0; i < accounts.length; i++) {
    const probe = accounts[(i + 1) % accounts.length];
    const response = getJSON(
      '/api/v1/wallets/' + accounts[i].walletId,
      probe.token,
      'setup_wallet_get_foreign',
      http.expectedStatuses(404)
    );
    if (response.status !== 404) {
      throw new Error(
        'wallet ' +
          accounts[i].walletId +
          ' of ' +
          accounts[i].email +
          ' answered ' +
          response.status +
          ' for ' +
          probe.email +
          '; it does not belong to the expected user'
      );
    }
  }
}

export function provision() {
  const accounts = [];
  for (let i = 1; i <= config.topology.senders; i++) {
    accounts.push(provisionAccount('sender', i));
  }
  for (let i = 1; i <= config.topology.receivers; i++) {
    accounts.push(provisionAccount('receiver', i));
  }
  const hot = provisionAccount('hot', 1);
  accounts.push(hot);

  const senders = [];
  const receivers = [];
  for (let i = 0; i < accounts.length; i++) {
    if (accounts[i].role === 'sender') {
      senders.push(accounts[i]);
    } else if (accounts[i].role === 'receiver') {
      receivers.push(accounts[i]);
    }
  }

  for (let i = 0; i < senders.length; i++) {
    fund(senders[i], config.funds.initialFunds);
  }
  fund(hot, config.funds.hotWalletFunds);

  verifyOwnership(accounts);

  console.log(
    'provisioned ' + accounts.length + ' accounts: ' + senders.length + ' senders at ' +
      config.funds.initialFunds + ', ' + receivers.length + ' receivers, 1 hot wallet at ' +
      config.funds.hotWalletFunds + ' (minor units, ' + CURRENCY + ')'
  );

  return {
    senders: senders,
    receivers: receivers,
    hot: hot,
    currency: CURRENCY,
    amount: config.funds.transferAmount,
  };
}
