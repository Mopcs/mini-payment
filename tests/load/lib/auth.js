// Registration and login for the dedicated load test accounts.
//
// A rerun must not fail because the accounts already exist, and it must not
// create a second set of them either, so registration falls back to login on
// 409. Tokens are returned to the caller and never logged.

import http from 'k6/http';
import { config } from './env.js';
import { body, errorCode, guardRateLimit } from './http.js';

function credentials(email) {
  return JSON.stringify({ email: email, password: config.credentials.password });
}

function requestOptions(tag) {
  return { headers: { 'Content-Type': 'application/json' }, tags: { name: tag } };
}

function accessToken(response, email) {
  const parsed = body(response);
  if (!parsed || typeof parsed.access_token !== 'string' || parsed.access_token === '') {
    throw new Error('authentication for ' + email + ' returned no access token');
  }
  return parsed.access_token;
}

function login(email) {
  const response = http.post(
    config.target.baseUrl + '/api/v1/auth/login',
    credentials(email),
    requestOptions('auth_login')
  );
  guardRateLimit(response, 'logging in load test users');
  if (response.status !== 200) {
    throw new Error(
      'logging in ' +
        email +
        ' failed with ' +
        response.status +
        ' ' +
        errorCode(response) +
        '. If LOAD_TEST_PASSWORD changed, the account still holds the old password; ' +
        'use a new LOAD_TEST_USER_PREFIX or reset the account.'
    );
  }
  return accessToken(response, email);
}

export function registerOrLogin(email) {
  // 409 email_already_registered is the normal answer for every run after the
  // first, so it is expected rather than a failed request.
  const registration = http.post(
    config.target.baseUrl + '/api/v1/auth/register',
    credentials(email),
    {
      headers: { 'Content-Type': 'application/json' },
      tags: { name: 'auth_register' },
      responseCallback: http.expectedStatuses(201, 409),
    }
  );
  guardRateLimit(registration, 'registering load test users');

  if (registration.status === 201) {
    return accessToken(registration, email);
  }
  // 409 email_already_registered: a previous run created this account, which is
  // the normal case for every run after the first.
  if (registration.status === 409) {
    return login(email);
  }
  throw new Error(
    'registering ' +
      email +
      ' failed with ' +
      registration.status +
      ' ' +
      errorCode(registration)
  );
}
