import http from 'k6/http';
import { check } from 'k6';
import { Trend } from 'k6/metrics';
import { PRODUCT_URL, cacheCounters, seedCatalogue } from './lib/setup.js';

// The hottest read in a catalogue: a single product by ID, served through the
// Redis decorator. This is the path the caching exists for, so it is the path
// worth measuring.
//
//   k6 run test/load/product_read.js
//
// To measure what the cache is actually worth, run it again against a product
// service started without REDIS_ADDR and compare. See README.md.

const readLatency = new Trend('product_read_duration', true);

export const options = {
  setupTimeout: '60s',
  scenarios: {
    catalogue_reads: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        // Warm up separately from the measured phase: the first requests pay
        // for connection setup and a cold cache, and averaging those into the
        // result flatters nothing and explains nothing.
        { duration: '10s', target: 20 },
        { duration: '30s', target: 100 },
        { duration: '10s', target: 0 },
      ],
      gracefulRampDown: '5s',
    },
  },
  thresholds: {
    // A read that misses the cache still has to be fast, so this is set where
    // a MongoDB round trip would still pass. It is a regression alarm, not a
    // performance target.
    http_req_duration: ['p(95)<150', 'p(99)<400'],
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
  },
};

export function setup() {
  const seeded = seedCatalogue();
  return { ...seeded, before: cacheCounters() };
}

export default function (data) {
  const res = http.get(`${PRODUCT_URL}/api/v1/products/${data.productId}`, {
    tags: { name: 'GET /products/{id}' },
  });

  readLatency.add(res.timings.duration);

  check(res, {
    'status is 200': (r) => r.status === 200,
    'the body is the product': (r) => r.json('id') === data.productId,
  });
}

export function teardown(data) {
  const after = cacheCounters();
  const hits = after.hit - data.before.hit;
  const misses = after.miss - data.before.miss;
  const errors = after.error - data.before.error;
  const total = hits + misses;

  if (total === 0) {
    console.log('cache: no lookups recorded — is REDIS_ADDR set on the product service?');
    return;
  }

  const rate = ((hits / total) * 100).toFixed(2);
  console.log(
    `cache: ${hits} hits, ${misses} misses, ${errors} errors — ${rate}% hit rate over ${total} lookups`,
  );
}
