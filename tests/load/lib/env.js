// Configuration shared by every scenario.
//
// Nothing here has a default target. BASE_URL must be given explicitly, and a
// host that is not loopback needs a second confirmation. A load test that
// silently points at production is the one mistake this file exists to prevent.
//
// Every value is read once, in the init context. k6 aborts before the first
// virtual user starts when a required value is missing, which is the "fail
// early" behaviour the suite promises.

const LOOPBACK_HOSTS = [
  'localhost',
  '127.0.0.1',
  '0.0.0.0',
  '::1',
  'host.docker.internal',
];

const MIN_PASSWORD_LENGTH = 12;

function fail(message) {
  throw new Error('load test configuration: ' + message);
}

function text(name, fallback) {
  const raw = __ENV[name];
  if (typeof raw === 'string' && raw.trim() !== '') {
    return raw.trim();
  }
  if (fallback === undefined) {
    fail(name + ' is required');
  }
  return fallback;
}

function integer(name, fallback, min, max) {
  const raw = text(name, fallback === undefined ? undefined : String(fallback));
  const value = Number(raw);
  if (!Number.isInteger(value) || value < min || value > max) {
    fail(name + ' must be an integer between ' + min + ' and ' + max + ', got "' + raw + '"');
  }
  return value;
}

function decimal(name, fallback, min, max) {
  const raw = text(name, fallback === undefined ? undefined : String(fallback));
  const value = Number(raw);
  if (!isFinite(value) || value < min || value > max) {
    fail(name + ' must be a number between ' + min + ' and ' + max + ', got "' + raw + '"');
  }
  return value;
}

function flag(name, fallback) {
  const value = text(name, fallback ? 'true' : 'false').toLowerCase();
  if (value !== 'true' && value !== 'false') {
    fail(name + ' must be true or false, got "' + value + '"');
  }
  return value === 'true';
}

// k6 accepts "30s", "1m", "1m30s", and rejects anything else at option parsing
// time. Checking here turns a typo into a configuration error with a name.
function duration(name, fallback) {
  const value = text(name, fallback);
  if (!/^[0-9]+(ms|s|m|h)([0-9]+(ms|s|m|h))*$/.test(value)) {
    fail(name + ' must be a k6 duration such as 30s, 2m, or 1m30s, got "' + value + '"');
  }
  return value;
}

function target(baseUrl) {
  const match = /^(https?):\/\/([^/:?#]+)(?::([0-9]+))?(\/.*)?$/i.exec(baseUrl);
  if (!match) {
    fail('BASE_URL must be an absolute http or https URL, got "' + baseUrl + '"');
  }

  const host = match[2].toLowerCase();
  if (host.indexOf('prod') !== -1) {
    fail('BASE_URL points at ' + host + '; load tests must never target production');
  }

  const local = LOOPBACK_HOSTS.indexOf(host) !== -1;
  if (!local && !flag('LOAD_TEST_ALLOW_REMOTE', false)) {
    fail(
      'BASE_URL host "' + host + '" is not loopback. Set LOAD_TEST_ALLOW_REMOTE=true ' +
        'only when it is a dedicated, isolated test environment.'
    );
  }
  return { baseUrl: baseUrl, host: host, local: local };
}

// "warmup:2:30s,low:10:1m" becomes ramping-vus stages. A compact string is
// easier to keep correct than five separate variables per stage.
function stages(name, fallback) {
  const raw = text(name, fallback);
  const parsed = raw.split(',').map(function (part) {
    const pieces = part.split(':');
    if (pieces.length !== 3) {
      fail(name + ' entries must look like label:vus:duration, got "' + part.trim() + '"');
    }
    const label = pieces[0].trim();
    const vus = Number(pieces[1].trim());
    if (!label || !Number.isInteger(vus) || vus < 0 || vus > 5000) {
      fail(name + ' entry "' + part.trim() + '" needs a label and a virtual user count');
    }
    const stageDuration = pieces[2].trim();
    if (!/^[0-9]+(ms|s|m|h)([0-9]+(ms|s|m|h))*$/.test(stageDuration)) {
      fail(name + ' entry "' + part.trim() + '" needs a k6 duration such as 30s or 2m');
    }
    return { label: label, target: vus, duration: stageDuration };
  });
  if (parsed.length === 0) {
    fail(name + ' must describe at least one stage');
  }
  return parsed;
}

const baseUrl = text('BASE_URL').replace(/\/+$/, '');
const targetUrl = target(baseUrl);

const password = text('LOAD_TEST_PASSWORD');
if (password.length < MIN_PASSWORD_LENGTH) {
  fail(
    'LOAD_TEST_PASSWORD must be at least ' +
      MIN_PASSWORD_LENGTH +
      ' characters; the API rejects shorter passwords'
  );
}

export const config = {
  target: targetUrl,

  credentials: {
    password: password,
    emailDomain: text('LOAD_TEST_EMAIL_DOMAIN', 'loadtest.example'),
    userPrefix: text('LOAD_TEST_USER_PREFIX', 'k6-load'),
  },

  // A fixed, deliberately small set of accounts. Registering users per
  // iteration would measure the registration path, not the payment path.
  topology: {
    senders: integer('LOAD_TEST_SENDERS', 8, 1, 40),
    receivers: integer('LOAD_TEST_RECEIVERS', 4, 1, 40),
    contentionVus: integer('LOAD_TEST_CONTENTION_VUS', 4, 0, 100),
  },

  // Amounts are minor units, exactly as the API stores them.
  funds: {
    currency: text('LOAD_TEST_CURRENCY', 'USD').toUpperCase(),
    initialFunds: integer('LOAD_TEST_INITIAL_FUNDS', 100000000, 1000, 9000000000000000000),
    hotWalletFunds: integer('LOAD_TEST_HOT_WALLET_FUNDS', 1000000000, 1000, 9000000000000000000),
    transferAmount: integer('LOAD_TEST_TRANSFER_AMOUNT', 1000, 1, 1000000000),
  },

  baseline: {
    vus: integer('LOAD_TEST_BASELINE_VUS', 3, 1, 100),
    duration: duration('LOAD_TEST_BASELINE_DURATION', '30s'),
    pause: decimal('LOAD_TEST_BASELINE_PAUSE', 1, 0, 60),
  },

  transfers: {
    vus: integer('LOAD_TEST_TRANSFER_VUS', 10, 1, 200),
    duration: duration('LOAD_TEST_TRANSFER_DURATION', '1m'),
    pause: decimal('LOAD_TEST_TRANSFER_PAUSE', 0, 0, 60),
  },

  idempotency: {
    repeats: integer('LOAD_TEST_IDEMPOTENCY_REPEATS', 5, 2, 50),
  },

  stress: {
    stages: stages(
      'LOAD_TEST_STRESS_STAGES',
      'warmup:2:30s,low:10:1m,medium:25:2m,high:50:2m,recovery:5:1m'
    ),
    readShare: decimal('LOAD_TEST_STRESS_READ_SHARE', 0.3, 0, 1),
    pause: decimal('LOAD_TEST_STRESS_PAUSE', 0, 0, 60),
  },

  // Defaults are deliberately loose. They are a regression guard, not a claim
  // about what the system can do; see the README for how to set them.
  thresholds: {
    httpFailedRate: decimal('THRESHOLD_HTTP_FAILED_RATE', 0.01, 0, 1),
    p95Ms: decimal('THRESHOLD_P95_MS', 300, 1, 60000),
    // The contention flow sends every virtual user from one wallet, so each
    // request waits for the previous one to release that row lock. Its latency
    // is therefore a queueing time, not a service time, and it gets a separate
    // budget: the point is to catch runaway lock waits, not to compare this
    // path against the uncontended one.
    contentionP95Ms: decimal('THRESHOLD_CONTENTION_P95_MS', 800, 1, 60000),
    p99Ms: decimal('THRESHOLD_P99_MS', 800, 1, 60000),
    // A stress run is meant to push the system past its comfortable capacity, so
    // holding it to the steady-state latency budget would only ever produce a red
    // result and teach everyone to ignore it. These are the budgets for that run:
    // loose enough to tolerate saturation, tight enough to catch collapse. The
    // failure-rate and success-rate thresholds still apply unchanged, because
    // "no errors at peak load" is a real invariant.
    stressP95Ms: decimal('THRESHOLD_STRESS_P95_MS', 2000, 1, 120000),
    stressP99Ms: decimal('THRESHOLD_STRESS_P99_MS', 5000, 1, 120000),
    transferSuccessRate: decimal('THRESHOLD_TRANSFER_SUCCESS_RATE', 0.99, 0, 1),
    businessRejected: integer('THRESHOLD_BUSINESS_REJECTED', 0, 0, 1000000),
  },

  output: {
    dir: text('LOAD_TEST_RESULTS_DIR', 'results'),
    command: text('LOAD_TEST_COMMAND', 'k6 run scenarios/<scenario>.js'),
  },
};

// Printed once at the start of a run so the operator can see what is about to
// be hammered, and that it only ever touches simulated funds.
export function banner(scenario) {
  return [
    'scenario:   ' + scenario,
    'target:     ' + config.target.baseUrl + (config.target.local ? ' (loopback)' : ' (remote, explicitly allowed)'),
    'funds:      simulated only, ' + config.funds.currency + ' wallets created by this suite',
    'accounts:   ' + (config.topology.senders + config.topology.receivers + 1) +
      ' dedicated load test users (prefix "' + config.credentials.userPrefix + '")',
  ].join('\n');
}
