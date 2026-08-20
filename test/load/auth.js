import http from 'k6/http';
import { check } from 'k6';
import { PASSWORD, USER_URL, registerAndLogin } from './lib/setup.js';

// Login, for contrast with product_read.js.
//
// This endpoint is slow on purpose. bcrypt is a deliberately expensive hash —
// that is the entire point of using it — so login is CPU-bound at a cost chosen
// to make offline cracking impractical. Measuring it against the same threshold
// as a cached read would be measuring the wrong thing, and "optimising" it by
// lowering the bcrypt cost would be trading security for a number.
//
// What this run is actually for:
//   - establishing the shape of the curve, so a regression is visible later
//   - showing where the ceiling is, which is a capacity-planning input
//   - confirming the token issued here is what makes every other request cheap
//
//   k6 run test/load/auth.js

export const options = {
  setupTimeout: '60s',
  scenarios: {
    logins: {
      executor: 'ramping-vus',
      startVUs: 0,
      // Far fewer VUs than the read test: this is CPU-bound, and piling on
      // concurrency past the number of cores only lengthens the queue.
      stages: [
        { duration: '10s', target: 5 },
        { duration: '20s', target: 20 },
        { duration: '5s', target: 0 },
      ],
      gracefulRampDown: '10s',
    },
  },
  thresholds: {
    // An order of magnitude looser than the read path, and deliberately so.
    // The number to watch is not whether it is fast but whether it moved.
    http_req_duration: ['p(95)<2000'],
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
  },
};

export function setup() {
  // One account, reused by every VU: the cost under test is verifying a
  // password, not creating accounts.
  return registerAndLogin();
}

export default function (data) {
  const res = http.post(
    `${USER_URL}/api/v1/auth/login`,
    JSON.stringify({ email: data.email, password: PASSWORD }),
    { headers: { 'Content-Type': 'application/json' }, tags: { name: 'POST /auth/login' } },
  );

  check(res, {
    'status is 200': (r) => r.status === 200,
    'a token comes back': (r) => String(r.json('token')).length > 20,
  });
}
