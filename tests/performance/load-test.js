import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate } from 'k6/metrics';

// Custom metrics
const errorRate = new Rate('errors');

// Test configuration
export const options = {
  stages: [
    { duration: '30s', target: 10000 },   // Ramp up to 10K RPS
    { duration: '1m', target: 10000 },    // Stay at 10K RPS
    { duration: '30s', target: 50000 },   // Ramp up to 50K RPS
    { duration: '1m', target: 50000 },    // Stay at 50K RPS
    { duration: '30s', target: 100000 },  // Ramp up to 100K RPS
    { duration: '2m', target: 100000 },   // Stay at 100K RPS
    { duration: '30s', target: 0 },       // Ramp down
  ],
  thresholds: {
    http_req_duration: ['p(95)<50'],      // 95% of requests under 50ms
    http_req_duration: ['p(99)<100'],     // 99% of requests under 100ms
    http_req_failed: ['rate<0.01'],       // Error rate under 1%
    errors: ['rate<0.01'],                 // Custom error rate under 1%
  },
  discardResponseBodies: true,
  scenarios: {
    constant_request_rate: {
      executor: 'constant-arrival-rate',
      rate: 100000,                        // 100K iterations per second
      duration: '2m',
      preAllocatedVUs: 1000,               // Pre-allocate VUs
      maxVUs: 5000,                        // Max VUs to scale to
    },
  },
};

const BASE_URL = 'http://localhost:8080';

// Test data
const endpoints = [
  { path: '/api/users', method: 'GET', weight: 30 },
  { path: '/api/products', method: 'GET', weight: 25 },
  { path: '/api/orders', method: 'GET', weight: 20 },
  { path: '/api/auth/login', method: 'POST', weight: 15 },
  { path: '/api/data', method: 'POST', weight: 10 },
];

const users = [
  { username: 'user1', password: 'pass1', token: 'token1' },
  { username: 'user2', password: 'pass2', token: 'token2' },
  { username: 'user3', password: 'pass3', token: 'token3' },
];

export function setup() {
  console.log('Starting large-scale load test...');
  console.log('Target: 100K+ RPS');
  console.log('Duration: 5 minutes total');
  console.log('Endpoints tested:', endpoints.length);
}

export default function () {
  // Select endpoint based on weight
  const endpoint = selectWeightedEndpoint(endpoints);
  const user = users[Math.floor(Math.random() * users.length)];
  
  let url = `${BASE_URL}${endpoint.path}`;
  let params = {
    headers: {
      'Content-Type': 'application/json',
      'User-Agent': 'k6-load-test',
      'X-Request-ID': generateRequestId(),
    },
  };

  // Add authentication for protected endpoints
  if (endpoint.path.includes('/api/auth') || endpoint.path.includes('/api/data')) {
    params.headers['Authorization'] = `Bearer ${user.token}`;
  }

  let payload = null;
  if (endpoint.method === 'POST') {
    payload = JSON.stringify({
      user_id: user.username,
      timestamp: Date.now(),
      data: generateRandomData(100), // 100KB payload
    });
  }

  // Make request
  let response;
  if (endpoint.method === 'GET') {
    response = http.get(url, params);
  } else {
    response = http.post(url, payload, params);
  }

  // Check response
  const success = check(response, {
    'status is 200': (r) => r.status === 200,
    'response time < 100ms': (r) => r.timings.duration < 100,
    'response time < 50ms': (r) => r.timings.duration < 50,
    'content length > 0': (r) => r.body.length > 0,
  });

  errorRate.add(!success);

  // Additional checks for specific endpoints
  if (endpoint.path === '/api/users') {
    check(response, {
      'users endpoint returns array': (r) => JSON.parse(r.body).constructor === Array,
    });
  }

  if (endpoint.path === '/api/auth/login') {
    check(response, {
      'auth endpoint returns token': (r) => JSON.parse(r.body).token !== undefined,
    });
  }

  // Small sleep to prevent overwhelming
  sleep(0.001); // 1ms
}

export function teardown() {
  console.log('Load test completed');
  console.log('Check results for performance metrics');
}

// Helper functions
function selectWeightedEndpoint(endpoints) {
  const totalWeight = endpoints.reduce((sum, ep) => sum + ep.weight, 0);
  let random = Math.random() * totalWeight;
  
  for (const endpoint of endpoints) {
    random -= endpoint.weight;
    if (random <= 0) {
      return endpoint;
    }
  }
  return endpoints[0];
}

function generateRequestId() {
  return 'req_' + Math.random().toString(36).substr(2, 9) + '_' + Date.now();
}

function generateRandomData(size) {
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
  let result = '';
  for (let i = 0; i < size; i++) {
    result += chars.charAt(Math.floor(Math.random() * chars.length));
  }
  return result;
}

// Custom metrics collection
export function handleSummary(data) {
  console.log('=== Load Test Summary ===');
  console.log(`Total requests: ${data.metrics.http_reqs.count}`);
  console.log(`Failed requests: ${data.metrics.http_req_failed.count}`);
  console.log(`Error rate: ${(data.metrics.http_req_failed.rate * 100).toFixed(2)}%`);
  console.log(`Average response time: ${data.metrics.http_req_duration.avg.toFixed(2)}ms`);
  console.log(`95th percentile: ${data.metrics.http_req_duration['p(95)'].toFixed(2)}ms`);
  console.log(`99th percentile: ${data.metrics.http_req_duration['p(99)'].toFixed(2)}ms`);
  
  // Performance thresholds check
  if (data.metrics.http_req_duration['p(95)'] > 50) {
    console.warn('⚠️ 95th percentile response time exceeded 50ms threshold');
  }
  
  if (data.metrics.http_req_failed.rate > 0.01) {
    console.warn('⚠️ Error rate exceeded 1% threshold');
  }
  
  if (data.metrics.http_reqs.count < 5000000) { // 5M requests for 100K RPS over 50s
    console.warn('⚠️ Total requests below expected for 100K RPS target');
  }
  
  return {
    'performance-summary.json': JSON.stringify(data, null, 2),
  };
}
