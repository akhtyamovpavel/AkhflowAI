import assert from "node:assert/strict";
import { randomBytes } from "node:crypto";

const base = process.env.AKHFLOW_TEST_SERVER_URL;
if (!base) {
  throw new Error("AKHFLOW_TEST_SERVER_URL must point to an isolated test server");
}
const suffix = randomBytes(12).toString("hex");
const registration = await fetch(`${base}/api/auth/register`, {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ email: `sse-${suffix}@example.invalid`, password: randomBytes(24).toString("hex") }),
});
assert.equal(registration.status, 201, "could not register test user");
const { token } = await registration.json();
const project = `sse-${suffix}`;
const created = await fetch(`${base}/api/projects`, {
  method: "POST",
  headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
  body: JSON.stringify({ name: "SSE test", slug: project }),
});
assert.equal(created.status, 201, "could not create test project");

for (const method of ["bearer", "query"]) {
  const url = new URL(`/api/projects/${project}/events`, base);
  const headers = {};
  if (method === "bearer") {
    headers.Authorization = `Bearer ${token}`;
  } else {
    url.searchParams.set("token", token);
  }
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 3000);
  let response;
  try {
    response = await fetch(url, { headers, signal: controller.signal });
  } catch {
    throw new Error(`SSE ${method} headers did not arrive within 3 seconds`);
  } finally {
    clearTimeout(timer);
  }
  assert.equal(response.status, 200, `SSE ${method} returned unexpected status`);
  assert.match(response.headers.get("content-type"), /^text\/event-stream/);
  await response.body.cancel();
}
console.log("PASS: nginx forwards SSE headers before the first event for Bearer and query auth");
