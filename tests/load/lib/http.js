// Thin wrappers over k6's HTTP client.
//
// Every request carries a `name` tag so thresholds can target one endpoint
// without matching on a URL that contains a wallet id.

import http from 'k6/http';
import { config } from './env.js';

export function authHeaders(token) {
  const headers = {};
  if (token) {
    headers.Authorization = 'Bearer ' + token;
  }
  return headers;
}

function jsonHeaders(token, extra) {
  const headers = { 'Content-Type': 'application/json' };
  if (token) {
    headers.Authorization = 'Bearer ' + token;
  }
  if (extra) {
    Object.keys(extra).forEach(function (key) {
      headers[key] = extra[key];
    });
  }
  return headers;
}

function endpoint(path) {
  return config.target.baseUrl + path;
}

export function getJSON(path, token, tag, responseCallback) {
  const params = { headers: authHeaders(token), tags: { name: tag } };
  if (responseCallback) {
    params.responseCallback = responseCallback;
  }
  return http.get(endpoint(path), params);
}

export function postJSON(path, payload, token, tag, extraHeaders, responseCallback) {
  const params = { headers: jsonHeaders(token, extraHeaders), tags: { name: tag } };
  if (responseCallback) {
    params.responseCallback = responseCallback;
  }
  return http.post(endpoint(path), JSON.stringify(payload), params);
}

// The API answers with {"error":{"code","message"}} on failure and with the
// resource on success, so an unparseable body means something outside the
// contract answered.
export function body(response) {
  try {
    return response.json();
  } catch (error) {
    return null;
  }
}

export function errorCode(response) {
  const parsed = body(response);
  if (parsed && parsed.error && parsed.error.code) {
    return String(parsed.error.code);
  }
  return '';
}

export function errorMessage(response) {
  const parsed = body(response);
  if (parsed && parsed.error && parsed.error.message) {
    return String(parsed.error.message);
  }
  return '';
}

// A 429 means the target still enforces a budget the suite cannot stay under.
// Continuing would measure the limiter instead of the payment path, so the run
// stops with the variable to change.
export function guardRateLimit(response, action) {
  if (response.status !== 429) {
    return;
  }
  const retryAfter = response.headers['Retry-After'] || response.headers['Retry-after'] || 'unknown';
  throw new Error(
    'rate limited while ' +
      action +
      ' (Retry-After: ' +
      retryAfter +
      '). Raise RATE_LIMIT_AUTH_LIMIT and RATE_LIMIT_FINANCIAL_LIMIT for the ' +
      'load environment; see tests/load/README.md.'
  );
}
