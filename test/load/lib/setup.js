import http from 'k6/http';
import { fail } from 'k6';

// Service addresses. Defaults match docker-compose.yml; override for a
// deployed environment with -e USER_URL=... and so on.
export const USER_URL = __ENV.USER_URL || 'http://127.0.0.1:8080';
export const SELLER_URL = __ENV.SELLER_URL || 'http://127.0.0.1:8081';
export const PRODUCT_URL = __ENV.PRODUCT_URL || 'http://127.0.0.1:8082';

export const PASSWORD = 'correct-horse-battery';

const JSON_HEADERS = { 'Content-Type': 'application/json' };

export function bearer(token) {
  return { headers: { ...JSON_HEADERS, Authorization: `Bearer ${token}` } };
}

// unique keeps runs from colliding: emails and shop names are unique per
// account, so a second run of a fixed value would fail on the constraint
// rather than on anything the test is trying to measure.
export function unique(prefix) {
  return `${prefix}-${Date.now()}-${Math.floor(Math.random() * 1e6)}`;
}

export function registerAndLogin() {
  const email = `${unique('load')}@example.com`;

  const created = http.post(
    `${USER_URL}/api/v1/auth/register`,
    JSON.stringify({ name: 'Load Tester', email, password: PASSWORD }),
    { headers: JSON_HEADERS },
  );
  if (created.status !== 201) {
    fail(`register failed: ${created.status} ${created.body}`);
  }

  const session = http.post(
    `${USER_URL}/api/v1/auth/login`,
    JSON.stringify({ email, password: PASSWORD }),
    { headers: JSON_HEADERS },
  );
  if (session.status !== 200) {
    fail(`login failed: ${session.status} ${session.body}`);
  }

  return { email, token: session.json('token'), userId: created.json('id') };
}

// seedCatalogue creates an account, a shop, and one product to read.
//
// Creating the product is retried because the two services are joined by an
// event rather than a call: the shop exists the moment the seller service
// answers, but the product service only learns about it when the event lands.
// A load test that ignored that would fail on its first run against a cold
// stack and pass on the second, which is the least useful kind of flake.
export function seedCatalogue() {
  const session = registerAndLogin();

  const shop = http.post(
    `${SELLER_URL}/api/v1/sellers`,
    JSON.stringify({ shop_name: unique('Load Shop') }),
    bearer(session.token),
  );
  if (shop.status !== 201) {
    fail(`shop creation failed: ${shop.status} ${shop.body}`);
  }

  const product = createProductWithRetry(session.token);

  return {
    token: session.token,
    sellerId: shop.json('id'),
    productId: product.id,
    eventLatencyMs: product.waitedMs,
  };
}

function createProductWithRetry(token, timeoutMs = 30000) {
  const body = JSON.stringify({
    name: 'Load Test Mug',
    description: 'seeded by k6',
    price_minor: 25000,
    currency: 'THB',
    stock: 1000,
  });

  const startedAt = Date.now();
  while (Date.now() - startedAt < timeoutMs) {
    const res = http.post(`${PRODUCT_URL}/api/v1/products`, body, bearer(token));
    if (res.status === 201) {
      return { id: res.json('id'), waitedMs: Date.now() - startedAt };
    }
    // 409 means the seller event has not arrived yet, which is expected for
    // the first moment after a shop is created. Anything else is a real error.
    if (res.status !== 409) {
      fail(`product creation failed: ${res.status} ${res.body}`);
    }
  }
  fail(`the seller event never reached the product service within ${timeoutMs}ms`);
}

// Admin ports carry the Prometheus metrics. They are separate listeners, so a
// load test can read them without its own traffic distorting the API's numbers.
export const PRODUCT_ADMIN_URL = __ENV.PRODUCT_ADMIN_URL || 'http://127.0.0.1:6062';

// counterValue pulls one labelled counter out of the exposition format.
//
// Reading the counter before and after a run turns "the cache helps" into a
// number, which is the difference between a claim and a measurement.
export function counterValue(metricsBody, metric, label) {
  const line = metricsBody
    .split('\n')
    .find((l) => l.startsWith(metric) && l.includes(label));
  if (!line) {
    return 0;
  }
  return Number(line.trim().split(/\s+/).pop()) || 0;
}

export function cacheCounters() {
  const res = http.get(`${PRODUCT_ADMIN_URL}/metrics`);
  if (res.status !== 200) {
    return { hit: 0, miss: 0, error: 0 };
  }
  return {
    hit: counterValue(res.body, 'product_cache_lookups_total', 'result="hit"'),
    miss: counterValue(res.body, 'product_cache_lookups_total', 'result="miss"'),
    error: counterValue(res.body, 'product_cache_lookups_total', 'result="error"'),
  };
}
