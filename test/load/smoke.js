import http from 'k6/http';
import { check } from 'k6';
import { PRODUCT_URL, SELLER_URL, USER_URL, seedCatalogue } from './lib/setup.js';

// A smoke test, not a load test: one user, a handful of requests, run before
// the real thing to prove the stack is wired rather than to measure it.
//
//   k6 run test/load/smoke.js
export const options = {
  vus: 1,
  iterations: 1,
  setupTimeout: '60s',
  thresholds: {
    // Nothing at all may fail at this volume. If it does, measuring throughput
    // is pointless until it is fixed.
    checks: ['rate==1.0'],
    http_req_failed: ['rate==0.0'],
  },
};

export function setup() {
  const seeded = seedCatalogue();
  // Logged rather than returned through handleSummary, which would replace
  // k6's own summary — and the checks and thresholds in it are the point.
  console.log(`seller event reached the product service in ${seeded.eventLatencyMs}ms`);
  return seeded;
}

export default function (data) {
  for (const [name, url] of [
    ['user', USER_URL],
    ['seller', SELLER_URL],
    ['product', PRODUCT_URL],
  ]) {
    const live = http.get(`${url}/healthz`);
    check(live, { [`${name} is alive`]: (r) => r.status === 200 });

    const ready = http.get(`${url}/readyz`);
    check(ready, { [`${name} is ready`]: (r) => r.status === 200 });
  }

  const product = http.get(`${PRODUCT_URL}/api/v1/products/${data.productId}`);
  check(product, {
    'the seeded product is readable': (r) => r.status === 200,
    'it carries the shop name the event delivered': (r) =>
      String(r.json('seller_name')).startsWith('Load Shop'),
  });

  const listing = http.get(`${PRODUCT_URL}/api/v1/products?limit=10`);
  check(listing, { 'the catalogue lists': (r) => r.status === 200 });
}
