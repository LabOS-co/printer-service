#!/usr/bin/env node
/*
 * Generates printgateway.postman_collection.json and env/*.postman_environment.json.
 *
 * NEVER EDIT THE GENERATED FILES — edit this script and re-run it, the same
 * convention docs/build_*.js already uses for the .docx deliverables:
 *
 *     node tests/postman/build-collection.js
 *
 * Why generated rather than hand-maintained: 176 scenarios, each needing the
 * same four boilerplate blocks (url object, auth header, envelope assertion,
 * profile gate). Hand-written, a scenario is ~25 lines of JSON in which the one
 * line that matters is buried; here it is one object with a `test` string. It
 * also makes the profile gates mechanical — a scenario declares `only:'s3'` and
 * the gate is generated identically everywhere, rather than being copy-pasted
 * 20 times with one copy subtly wrong.
 *
 * Scenario IDs are the contract with TEST-PLAN.md: every request name starts
 * with its ID from that document. checkPlanCoverage() below fails the build if
 * this file and the plan disagree.
 */

'use strict';

const fs = require('fs');
const path = require('path');

const TESTS_DIR = path.resolve(__dirname, '..');
const OUT_COLLECTION = path.join(__dirname, 'printgateway.postman_collection.json');
const OUT_ENV_DIR = path.join(__dirname, 'env');
const PROFILES = JSON.parse(fs.readFileSync(path.join(TESTS_DIR, 'profiles.json'), 'utf8'));

/* ------------------------------------------------------------------ *
 * Shared test helpers.
 *
 * Injected once as a collection variable and eval'd at the top of every
 * test script.
 *
 * Declared as IMPLICIT globals — bare `name = function ...`, no var/let and no
 * global object — on purpose. Two more obvious spellings both fail:
 *
 *   globalThis.val = ...   ReferenceError: globalThis is not defined.
 *                          Postman's sandbox (uvm) does not expose it.
 *                          Verified against newman 6.2.2 — every single test
 *                          script errored out on the first line.
 *   var val = ...          eval'd inside a function scope would confine the
 *                          helpers to that scope, so the IIFE below could not
 *                          see them.
 *
 * A bare assignment in sloppy mode creates a property on the sandbox's own
 * global object without naming it, which is the one form that works in both
 * the Postman app and Newman.
 * ------------------------------------------------------------------ */
const UTILS = `
val = function (n) { var v = pm.variables.get(n); return v === undefined || v === null ? '' : String(v); };
flag = function (n) { return val(n).toLowerCase() === 'true'; };

// The ONLY discriminating part of a failure body. error_handler@v1.2.4 hardcodes
// errorCode "21" and errorMessage "Internal server error" on every failure
// regardless of status, so nothing may ever assert on those two (TEST-PLAN 3.1).
details = function () {
  try { return String(pm.response.json().errorDetails.details || ''); }
  catch (e) { return pm.response.text(); }
};

expectStatus = function (s) {
  pm.test('HTTP ' + s, function () { pm.response.to.have.status(s); });
};

expectFail = function (s, sub) {
  expectStatus(s);
  if (sub) {
    pm.test('details contains "' + sub + '"', function () {
      pm.expect(details()).to.include(sub);
    });
  }
};

expectAnyStatus = function (list) {
  pm.test('HTTP one of [' + list.join(', ') + '] (got ' + pm.response.code + ')', function () {
    pm.expect(list).to.include(pm.response.code);
  });
};

expectSubmitted = function () {
  expectStatus(200);
  pm.test('body is a submitted-print envelope', function () {
    var j = pm.response.json();
    pm.expect(j.status).to.eql('submitted');
    // lp's own acknowledgement, e.g. "request id is vp1-42 (0 file(s))".
    //
    // ZERO, not one. cups.LPSubmitter pipes the document on stdin and passes
    // no path operand at all, deliberately — that is what makes filename-based
    // argument injection structurally impossible rather than merely guarded
    // against. lp counts file OPERANDS, so a stdin job legitimately reports 0.
    // Asserted exactly rather than loosely: if the path operand were ever
    // reintroduced this count would silently become 1, and that regression is
    // worth catching here.
    // (src/printgateway/README.md documented "(1 file(s))" until this suite
    // caught it — that example predated the stdin change.)
    pm.expect(j.output).to.match(/request id is \\S+-\\d+ \\(0 file\\(s\\)\\)/);
  });
};

// A 500/502 must never carry the apperr.Internal detail: a spool path, lp's
// stderr, an S3 endpoint or a presigned URL. This is a security assertion, not
// a cosmetic one (TEST-PLAN 3.4).
expectNoLeak = function () {
  pm.test('no internal detail leaked into the response', function () {
    var b = pm.response.text();
    pm.expect(b, 'temp path').to.not.match(/\\/tmp\\//);
    pm.expect(b, 'spool filename').to.not.match(/print-(upload|download|s3)-/);
    pm.expect(b, 'lp stderr').to.not.match(/\\blp:/);
    pm.expect(b, 'credential-ish').to.not.match(/X-Amz-Signature|SecretKey|AccessKey/i);
  });
};

skip = function (why) {
  pm.test.skip('SKIPPED — ' + why, function () {});
};

// The statuses a print-by-s3_key may legitimately return under the CURRENT
// profile. Used by the rows whose subject is something else entirely (the JSON
// decoder's case-insensitivity, its duplicate-key rule) but which have to name
// a document somehow, and so inherit the store's health as well.
//
// Three cases, not two: "storage not configured" (503) and "storage configured
// and healthy" (200) are the obvious pair, but the C-fault-* profiles are
// configured AND deliberately broken, and answer 502 — which used to fail
// GW-JSON-08/09 under all three fault profiles for a reason having nothing to
// do with what those rows assert.
s3PrintStatuses = function () {
  if (!flag('s3Enabled')) return [503];
  if (val('s3Fault') !== 'none') return [502, 504];
  return [200];
};

// expires_at within tolerance of now+ttl. Generous by design: presigning can
// cost a live bucket-location round trip when S3_REGION is empty.
expectExpiry = function (ttlSeconds, toleranceSeconds) {
  pm.test('expires_at is about ' + ttlSeconds + 's away', function () {
    var j = pm.response.json();
    var delta = (Date.parse(j.expires_at) - Date.now()) / 1000;
    pm.expect(delta, 'seconds until expiry').to.be.within(
      ttlSeconds - (toleranceSeconds || 90),
      ttlSeconds + (toleranceSeconds || 90)
    );
  });
};

expectPresigned = function (key) {
  expectStatus(200);
  pm.test('presign envelope', function () {
    var j = pm.response.json();
    pm.expect(j.url, 'url').to.be.a('string').and.to.match(/^https?:\\/\\//);
    pm.expect(j.key, 'key echoed verbatim').to.eql(key);
    pm.expect(j.expires_at, 'expires_at').to.be.a('string');
  });
};
`.trim();

/* ------------------------------------------------------------------ *
 * Profile gates. A scenario declares which capability it needs; the gate
 * is generated identically for every scenario that needs it.
 * ------------------------------------------------------------------ */
const GATES = {
  s3:        { cond: "!flag('s3Enabled')",                    why: 'needs a profile with object storage (C / D / G)' },
  noS3:      { cond: "flag('s3Enabled')",                     why: 'asserts the storage-disabled path; run under profile A' },
  fixtures:  { cond: "!flag('fixturesUp')",                   why: 'needs the fixture HTTP server (profile E / G)' },
  permissive:{ cond: "!flag('allowPrivate')",                 why: 'needs PRINT_GATEWAY_ALLOW_PRIVATE_TARGETS=true (profile E / G)' },
  strict:    { cond: "flag('allowPrivate')",                  why: 'asserts the strict fetch guard; run under profile A' },
  allowlist: { cond: "!flag('allowlistOn')",                  why: 'needs PRINT_GATEWAY_FETCH_ALLOWED_HOSTS (profile F / E-allowlist)' },
  // The allowlist is checked BEFORE the address block (fetch.go: validateURL →
  // port → host → dial control). So under an allowlist profile every scenario
  // whose target host is not on the list answers 403 and never reaches the gate
  // it was written to exercise. Those scenarios must therefore run only where
  // no allowlist is configured — which is a fact about gate ORDER, worth
  // recording here rather than rediscovering from eight confusing 403s.
  noAllowlist: { cond: "flag('allowlistOn')",                 why: 'the host allowlist answers 403 before this gate is reached; run under profile A / E' },
  limits:    { cond: "!flag('tightLimits')",                  why: 'needs the tight-limit profile G' },
  paper:     { cond: "!flag('paper')",                        why: 'prints on real paper; pass --with-paper to run it' },
  vault:     { cond: "!flag('vaultEnabled')",                 why: 'needs a Vault-backed profile (B / D)' },
  faultEndpoint: { cond: "val('s3Fault') !== 'endpoint'",     why: 'needs profile C-fault-endpoint' },
  faultCreds:    { cond: "val('s3Fault') !== 'creds'",        why: 'needs profile C-fault-creds' },
  faultSlow:     { cond: "val('s3Fault') !== 'slow'",         why: 'needs profile C-fault-slow' },
  healthyS3: { cond: "!flag('s3Enabled') || val('s3Fault') !== 'none'", why: 'needs a healthy object store (profile C / D / G)' },
  backendAws:   { cond: "val('s3Backend') !== 'aws'",         why: 'needs --s3-backend aws (a public presigned URL)' },
  backendLocal: { cond: "val('s3Backend') !== 'local'",       why: 'needs --s3-backend local' },
  defaultLimits: { cond: "flag('tightLimits')",             why: 'the tightened fetch cap rejects the response up front, before the behaviour under test occurs' },
  manual:    { cond: 'true',                                  why: 'not assertable over HTTP — see the named script' }
};

/* ------------------------------------------------------------------ *
 * Request construction
 * ------------------------------------------------------------------ */
const K = PROFILES.objectKeys;

function jsonBody(obj) {
  return typeof obj === 'string' ? obj : JSON.stringify(obj);
}

function buildUrl(p, exact) {
  // host as a single {{baseUrl}} segment is the form Postman itself emits for
  // a variable-rooted URL; splitting it into scheme/host/port would break the
  // moment a profile pointed baseUrl somewhere else.
  //
  // `exact` suppresses the parsed host/path pair and hands Postman the raw
  // string only. Needed wherever the literal path matters: given path segments
  // ["print", ""] Postman normalises the empty trailing segment away and sends
  // /print, so the trailing-slash scenario silently tested the wrong URL and
  // got a 415 where the server really does answer 404.
  const clean = p.replace(/^\//, '');
  if (exact) return { raw: '{{baseUrl}}/' + clean };
  return {
    raw: '{{baseUrl}}/' + clean,
    host: ['{{baseUrl}}'],
    path: clean === '' ? [] : clean.split('/')
  };
}

function scenario(s) {
  const headers = [];

  // auth: 'token' (default) | 'alt' | 'malformed' | 'none'
  const auth = s.auth === undefined ? 'token' : s.auth;
  if (auth === 'token') headers.push({ key: 'X-Labos-Print-Token', value: '{{token}}' });
  else if (auth === 'alt') headers.push({ key: 'X-Labos-Print-Token', value: '{{altToken}}' });
  else if (auth === 'malformed') headers.push({ key: 'X-Labos-Print-Token', value: '{{token}}x' });

  if (s.ctype) headers.push({ key: 'Content-Type', value: s.ctype });
  (s.headers || []).forEach(h => headers.push(h));

  let body;
  if (s.formdata) {
    body = { mode: 'formdata', formdata: s.formdata };
  } else if (s.raw !== undefined) {
    body = { mode: 'raw', raw: jsonBody(s.raw), options: { raw: { language: 'json' } } };
  }

  // Gate first, then the scenario's own assertions, all inside one IIFE so a
  // gated-out scenario reports as skipped rather than failing on a response
  // shape its profile was never going to produce.
  const lines = ["eval(pm.variables.get('utils'));", '(function () {'];
  (s.only || []).forEach(g => {
    const gate = GATES[g];
    if (!gate) throw new Error(`${s.id}: unknown gate "${g}"`);
    lines.push(`  if (${gate.cond}) { return skip(${JSON.stringify(gate.why)}); }`);
  });
  String(s.test).trim().split('\n').forEach(l => lines.push('  ' + l.trim()));
  lines.push('})();');

  const item = {
    name: `${s.id} · ${s.name}`,
    request: {
      method: s.method || 'POST',
      header: headers,
      // A parseable placeholder for external requests; the pre-request script
      // replaces it with the chained URL (see below for why it cannot simply
      // be "{{var}}").
      url: s.external
        ? { raw: 'http://chain-did-not-run.invalid/', protocol: 'http', host: ['chain-did-not-run', 'invalid'], path: [''] }
        : buildUrl(s.path === undefined ? '/print' : s.path, s.exactPath),
      description: s.desc || ''
    },
    event: [{ listen: 'test', script: { type: 'text/javascript', exec: lines } }]
  };
  if (body) item.request.body = body;

  let pre = s.pre ? String(s.pre).trim().split('\n').map(l => l.trim()) : [];

  // An external request addresses a URL produced by an earlier step and held in
  // a collection variable. Two things have to happen in a pre-request script,
  // neither of which the URL field can do on its own:
  //
  //  1. SKIP when the chain did not run — no object store in this profile, or
  //     the presign step failed. The variable is then empty and Postman raises
  //     a bare "request url is empty" error with no scenario name on it. A test
  //     script cannot decline to send a request, so the skip must happen here.
  //
  //  2. SET the URL via pm.request.url.update(). A url of {raw: "{{var}}"} —
  //     entirely a variable, with no parseable host — is NOT resolved by the
  //     SDK: the request goes out with no host and the test sees an undefined
  //     response code. Verified against newman 6.2.2. The declared URL below is
  //     therefore a parseable placeholder that this overwrites at run time.
  if (s.external) {
    const varName = (s.externalUrl.match(/\{\{(\w+)\}\}/) || [])[1];
    pre = [
      "eval(pm.variables.get('utils'));",
      `var chained = String(pm.variables.get(${JSON.stringify(varName)}) || '');`,
      'var blocked = ' + ((s.only || []).map(g => GATES[g].cond).join(' || ') || 'false') + ';',
      "if (blocked || !/^https?:\\/\\//.test(chained)) {",
      "  if (pm.execution && pm.execution.skipRequest) { pm.execution.skipRequest(); }",
      '} else {',
      '  pm.request.url.update(chained);',
      '}'
    ].concat(pre);
  }

  if (pre.length) {
    item.event.unshift({
      listen: 'prerequest',
      script: { type: 'text/javascript', exec: pre }
    });
  }
  return item;
}

/* Convenience builders for the three intakes. */
const printJSON = o => ({ ctype: 'application/json', raw: o });
const upload = (printer, file) => ({
  formdata: [
    { key: 'printer', value: printer, type: 'text' },
    { key: 'file', type: 'file', src: 'testdata/' + file }
  ]
});

/* ================================================================== *
 * SCENARIOS
 * ================================================================== */

const folders = [];
const F = (name, description, items) => folders.push({ name, description, item: items.map(scenario) });

/* ---------------- 01 Auth and routing ---------------- */
F('Auth and routing',
  'Authentication, method/route dispatch, and the X-Laas-Identifier correlation id. Nothing here contacts a printer, so it is safe to run against any profile at any time.',
[
  { id: 'GW-AUTH-01', name: 'No token → 401', auth: 'none',
    desc: 'Prerequisite: gateway running under any profile.',
    test: `expectFail(401, 'unauthorized');
           pm.test('correlation id present even on a 401', function () {
             pm.expect(pm.response.headers.has('X-Laas-Identifier')).to.be.true;
           });` },

  { id: 'GW-AUTH-02', name: 'Wrong token → 401', auth: 'alt',
    desc: 'Under a Vault profile the wrong token IS the environment token, so a pass here also proves Vault won the precedence contest (GW-SEC-02).',
    test: `expectFail(401, 'unauthorized');` },

  { id: 'GW-AUTH-04', name: 'Right prefix, wrong suffix → 401', auth: 'malformed',
    desc: 'Constant-time compare: a token sharing a prefix with the real one must fare no better than a random one.',
    test: `expectFail(401, 'unauthorized');` },

  { id: 'GW-AUTH-03', name: 'Valid token passes auth → 415 not 401', ctype: 'text/plain', raw: 'x',
    desc: 'The positive control for the whole auth layer: proves 415 below is reached, i.e. the token was accepted.',
    test: `expectFail(415, 'Content-Type must be');
           pm.test('not a 401', function () { pm.expect(pm.response.code).to.not.eql(401); });` },

  { id: 'GW-AUTH-05', name: '/files/presign without token → 401', path: '/files/presign', auth: 'none',
    ctype: 'application/json', raw: { key: '{{s3Key}}' },
    desc: 'Both routes are token-guarded; presign is not a lesser-privilege endpoint (it mints object-store credentials).',
    test: `expectFail(401, 'unauthorized');` },

  { id: 'GW-AUTH-06', name: 'GET /print → 405', method: 'GET',
    test: `expectFail(405, 'use POST');` },

  { id: 'GW-AUTH-07', name: 'PUT /files/presign → 405', method: 'PUT', path: '/files/presign',
    ctype: 'application/json', raw: { key: '{{s3Key}}' },
    test: `expectFail(405, 'use POST');` },

  { id: 'GW-AUTH-08', name: 'Unknown route → 404 plain text', method: 'GET', path: '/nope', auth: 'none',
    desc: 'requireToken is per-route, so an unknown path is NOT authenticated and answers ServeMux\'s own plain-text 404 rather than the labOS JSON envelope. Intended; asserted so a future catch-all cannot change it silently.',
    test: `expectStatus(404);
           pm.test('plain text, not the JSON envelope', function () {
             pm.expect(pm.response.text()).to.include('404 page not found');
             pm.expect(pm.response.text()).to.not.include('errorCode');
           });` },

  { id: 'GW-AUTH-09', name: 'Subpath /print/anything → 404', path: '/print/anything',
    desc: '/print is an exact ServeMux pattern, not a subtree — no route below it exists. The literal trailing-slash form ("/print/") cannot be expressed through Postman\'s URL model at all: parsed, it normalises the empty final segment away and sends /print (a 415); raw-only, the SDK fails to build a request. Verified separately with curl that /print/ is also a 404, so nothing is lost by asserting the subtree form here.',
    test: `expectStatus(404);` },

  { id: 'GW-AUTH-10', name: 'text/plain → 415', ctype: 'text/plain', raw: 'printer=x',
    test: `expectFail(415, 'Content-Type must be');` },

  { id: 'GW-AUTH-11', name: 'No Content-Type → 415', raw: 'printer=x',
    headers: [{ key: 'Content-Type', value: '', disabled: false }],
    test: `expectFail(415, 'Content-Type must be');` },

  { id: 'GW-AUTH-12', name: 'Uppercase multipart Content-Type is still multipart',
    ctype: 'MULTIPART/FORM-DATA; boundary=xyz', raw: 'not actually multipart',
    desc: 'Dispatch is by mime.ParseMediaType, not a literal lowercase prefix — so this must reach the multipart handler (400 on the bad body), never fall through to 415.',
    test: `expectFail(400, 'invalid multipart body');` },

  { id: 'GW-ID-01', name: 'Supplied correlation id is echoed', auth: 'none',
    headers: [{ key: 'X-Laas-Identifier', value: 'e2e-abc-123' }],
    test: `pm.test('id echoed verbatim', function () {
             pm.expect(pm.response.headers.get('X-Laas-Identifier')).to.eql('e2e-abc-123');
           });` },

  { id: 'GW-ID-02', name: 'Absent id is generated', auth: 'none',
    test: `pm.test('generated id', function () {
             pm.expect(pm.response.headers.get('X-Laas-Identifier')).to.match(/^req-/);
           });` },

  { id: 'GW-ID-03', name: 'Non-ASCII id is rejected and replaced', auth: 'none',
    headers: [{ key: 'X-Laas-Identifier', value: 'abcdef' }],
    desc: 'U+0085 (NEL) is a line terminator to many log consumers and is exactly what the byte-wise 0x20..0x7e allowlist exists to stop. Node permits \\x80-\\xff in a header value, so this actually reaches the server, unlike a C0 control which the client itself would refuse to send.',
    test: `pm.test('rejected, replaced with a generated id', function () {
             var got = pm.response.headers.get('X-Laas-Identifier');
             pm.expect(got).to.not.include('def');
             pm.expect(got).to.match(/^req-/);
           });` },

  { id: 'GW-ID-04', name: 'Over-length id is rejected and replaced', auth: 'none',
    pre: `pm.variables.set('longId', 'x'.repeat(129));`,
    headers: [{ key: 'X-Laas-Identifier', value: '{{longId}}' }],
    test: `pm.test('129 chars rejected', function () {
             var got = pm.response.headers.get('X-Laas-Identifier');
             pm.expect(got).to.match(/^req-/);
             pm.expect(got.length).to.be.below(129);
           });` },

  { id: 'GW-ID-06', name: 'Exactly 128 chars is accepted (boundary)', auth: 'none',
    pre: `pm.variables.set('maxId', 'y'.repeat(128));`,
    headers: [{ key: 'X-Laas-Identifier', value: '{{maxId}}' }],
    test: `pm.test('128 chars echoed verbatim', function () {
             pm.expect(pm.response.headers.get('X-Laas-Identifier')).to.eql('y'.repeat(128));
           });` },

  { id: 'GW-ID-05', name: 'Correlation id survives a 401', auth: 'alt',
    headers: [{ key: 'X-Laas-Identifier', value: 'e2e-on-401' }],
    test: `expectStatus(401);
           pm.test('id echoed on the rejection too', function () {
             pm.expect(pm.response.headers.get('X-Laas-Identifier')).to.eql('e2e-on-401');
           });` }
]);

/* ---------------- 02 Multipart intake ---------------- */
F('Multipart intake',
  'Option 1 of the contract: the caller attaches the document. This is the primary, never-deprecated intake path — the HLD is explicit that not every Windows caller has an S3 SDK. Prints go to {{printer}} (a virtual ippeveprinter queue), so no paper is consumed.',
[
  { id: 'GW-MP-01', name: 'Happy path → 200 submitted', ...upload('{{printer}}', 'small.pdf'),
    desc: 'Prerequisite: CUPS queue {{printer}} exists and is enabled (lpstat -p).',
    test: `expectSubmitted();` },

  { id: 'GW-MP-03', name: 'Missing printer field → 400',
    formdata: [{ key: 'file', type: 'file', src: 'testdata/small.pdf' }],
    test: `expectFail(400, 'missing url parameter: printer');` },

  { id: 'GW-MP-04', name: 'Missing file part → 400',
    formdata: [{ key: 'printer', value: '{{printer}}', type: 'text' }],
    test: `expectFail(400, 'missing file part');` },

  { id: 'GW-MP-05', name: 'Empty printer value → 400',
    formdata: [
      { key: 'printer', value: '', type: 'text' },
      { key: 'file', type: 'file', src: 'testdata/small.pdf' }
    ],
    test: `expectFail(400, 'missing url parameter: printer');` },

  { id: 'GW-MP-06', name: 'Nonexistent queue → 500, nothing leaked', ...upload('there-is-no-such-queue', 'small.pdf'),
    desc: 'The apperr Public/Internal split under load: lp\'s stderr names the queue and the spool path, and none of it may reach the caller.',
    test: `expectFail(500, 'print submission failed');
           expectNoLeak();
           pm.test('details is exactly the public text', function () {
             pm.expect(details()).to.eql('print submission failed');
           });` },

  { id: 'GW-MP-07', name: 'Multipart with no boundary → 400',
    ctype: 'multipart/form-data', raw: 'garbage',
    test: `expectFail(400, 'invalid multipart body');` },

  { id: 'GW-MP-10', name: 'Non-PDF content → 200 (documented gap)', ...upload('{{printer}}', 'not-a-pdf.txt'),
    desc: 'ASSERTS A KNOWN GAP (README "No content validation of a fetched or uploaded document"). If this ever starts failing, content validation was added — update the README and this row together, do not "fix" the expectation.',
    test: `expectSubmitted();` },

  { id: 'GW-MP-11', name: 'Empty file → 500 (lp refuses it, not the gateway)', ...upload('{{printer}}', 'empty.pdf'),
    desc: 'Verified live: a zero-byte document is accepted and spooled by the gateway — which validates nothing — and then rejected by lp itself ("lp: No file in print request."), surfacing as the generic 500. So the README\'s "a 200 response with an empty or non-PDF body is spooled and handed to lp as a success" holds for non-PDF (GW-MP-10) but NOT for empty. The gap is real; its blast radius is smaller than documented.',
    test: `expectFail(500, 'print submission failed');
           expectNoLeak();` },

  { id: 'GW-MP-12', name: 'Hostile filename is sanitized', ...upload('{{printer}}', 'weird-name.pdf'),
    desc: 'The spool filename is built from the caller-supplied part name. Verify the sanitized name in the gateway log (journalctl -u printgateway); the response cannot show it.',
    test: `expectSubmitted();` },

  { id: 'GW-MP-13', name: 'Unknown extra form field → 200',
    formdata: [
      { key: 'printer', value: '{{printer}}', type: 'text' },
      { key: 'file', type: 'file', src: 'testdata/small.pdf' },
      { key: 'bogus', value: '1', type: 'text' }
    ],
    desc: 'Multipart intake is deliberately NOT strict, unlike the JSON one — asserted so the asymmetry is intentional and visible.',
    test: `expectSubmitted();` },

  { id: 'GW-MP-14', name: 'Two file parts → first wins',
    formdata: [
      { key: 'printer', value: '{{printer}}', type: 'text' },
      { key: 'file', type: 'file', src: 'testdata/small.pdf' },
      { key: 'file', type: 'file', src: 'testdata/weird-name.pdf' }
    ],
    test: `expectSubmitted();` }
]);

/* ---------------- 03 JSON contract ---------------- */
F('JSON contract',
  'Strict JSON decoding, shared by POST /print (by-reference form) and POST /files/presign. Two properties are deliberately NOT enforced and are asserted as such: case-insensitive field matching, and duplicate-key last-wins.',
[
  { id: 'GW-JSON-01', name: 'Malformed JSON → 400', ...printJSON('{ not valid'),
    test: `expectFail(400, 'invalid JSON body');` },

  { id: 'GW-JSON-02', name: 'Unknown field → 400', ...printJSON({ printer: '{{printer}}', fiel_url: 'https://example.com/a.pdf' }),
    desc: 'A mistyped field name must name the typo, not fail downstream with the confusing "exactly one of file_url or s3_key is required".',
    test: `expectFail(400, 'invalid JSON body');
           pm.test('names the unknown field', function () {
             pm.expect(details()).to.include('unknown field');
           });` },

  { id: 'GW-JSON-03', name: 'Two concatenated JSON values → 400',
    ...printJSON('{"printer":"{{printer}}","s3_key":"a"}{"x":1}'),
    test: `expectFail(400, 'body must contain exactly one JSON value');` },

  { id: 'GW-JSON-04', name: 'Trailing garbage after JSON → 400',
    ...printJSON('{"printer":"{{printer}}","s3_key":"a"} oops'),
    test: `expectFail(400);` },

  { id: 'GW-JSON-05', name: 'Missing printer → 400', ...printJSON({ file_url: 'https://example.com/a.pdf' }),
    test: `expectFail(400, 'printer is required');` },

  { id: 'GW-JSON-06', name: 'Neither file_url nor s3_key → 400', ...printJSON({ printer: '{{printer}}' }),
    test: `expectFail(400, 'exactly one of file_url or s3_key is required');` },

  { id: 'GW-JSON-07', name: 'Both file_url and s3_key → 400',
    ...printJSON({ printer: '{{printer}}', file_url: 'https://example.com/a.pdf', s3_key: '{{s3Key}}' }),
    desc: 'Mixing them would leave one silently ignored — and the write-timeout budget assumes only one download per request.',
    test: `expectFail(400, 'exactly one of file_url or s3_key is required');` },

  { id: 'GW-JSON-08', name: 'Field names are case-insensitive (documented gap)',
    ...printJSON('{"Printer":"{{printer}}","S3_Key":"{{s3Key}}"}'),
    desc: 'ASSERTS A DOCUMENTED BEHAVIOUR: DisallowUnknownFields inherits encoding/json\'s case-insensitive matching. So this is accepted — 503 without storage, 200 with — and must NOT be 400.',
    test: `pm.test('accepted, not rejected as an unknown field', function () {
             pm.expect(pm.response.code, 'a 400 here would mean case-sensitivity changed').to.not.eql(400);
           });
           expectAnyStatus(s3PrintStatuses());` },

  { id: 'GW-JSON-09', name: 'Duplicate key, last wins (documented gap)',
    ...printJSON('{"printer":"no-such-queue","printer":"{{printer}}","s3_key":"{{s3Key}}"}'),
    desc: 'ASSERTS A DOCUMENTED BEHAVIOUR: encoding/json applies the last occurrence. The first value names a queue that does not exist, so a 500 here would mean the FIRST one won.',
    test: `pm.test('resolved to the last value', function () {
             pm.expect(details(), 'a submit failure means the first printer won').to.not.include('print submission failed');
           });
           expectAnyStatus(s3PrintStatuses());` },

  { id: 'GW-JSON-11', name: 'Empty body → 400', ctype: 'application/json', raw: '',
    test: `expectFail(400, 'invalid JSON body');` },

  { id: 'GW-JSON-12', name: 'JSON null body → 400', ...printJSON('null'),
    test: `expectFail(400, 'printer is required');` },

  { id: 'GW-JSON-13', name: 'charset parameter still routes as JSON',
    ctype: 'application/json; charset=utf-8', raw: JSON.stringify({ printer: '{{printer}}' }),
    test: `expectFail(400, 'exactly one of file_url or s3_key is required');
           pm.test('not 415', function () { pm.expect(pm.response.code).to.not.eql(415); });` }
]);

/* ---------------- 04 file_url intake ---------------- */
F('file_url intake',
  'Option 2: the server fetches a caller-supplied URL. EVERY 4xx row here is a security assertion — a 200 where a 4xx is expected is a live SSRF hole, not a test bug. Rows gated "strict" run under profile A; rows gated "permissive" need profile E (fixture server up).',
[
  { id: 'GW-URL-01', name: 'Happy path via fixture server → 200', only: ['permissive', 'fixtures', 'noAllowlist'],
    ...printJSON({ printer: '{{printer}}', file_url: '{{fixtureBase}}/small.pdf' }),
    desc: 'Uses small.pdf, not printDemo.pdf, so the happy path also holds under profile G\'s tightened fetch limit. Prerequisite: scripts/fixtures-server.sh start. Only possible with the guard relaxed — see TEST-PLAN 4.1.',
    test: `expectSubmitted();` },

  { id: 'GW-URL-02', name: 'Loopback address blocked → 400', only: ['strict', 'noAllowlist'],
    ...printJSON({ printer: '{{printer}}', file_url: 'http://127.0.0.1/x.pdf' }),
    test: `expectFail(400, 'file_url resolved to a disallowed address');` },

  { id: 'GW-URL-03', name: 'CUPS admin port blocked → 400', only: ['strict'],
    ...printJSON({ printer: '{{printer}}', file_url: 'http://127.0.0.1:631/admin' }),
    desc: 'The single rule that kills the original P0-4 finding. The port gate runs before the address gate, so this reports the port, not the address.',
    test: `expectFail(400, 'file_url port 631 is not allowed');` },

  { id: 'GW-URL-04', name: 'Cloud metadata endpoint blocked → 400', only: ['strict', 'noAllowlist'],
    ...printJSON({ printer: '{{printer}}', file_url: 'http://169.254.169.254/latest/meta-data/' }),
    test: `expectFail(400, 'file_url resolved to a disallowed address');` },

  { id: 'GW-URL-05', name: 'RFC1918 address blocked → 400', only: ['strict', 'noAllowlist'],
    ...printJSON({ printer: '{{printer}}', file_url: 'http://192.168.252.210/' }),
    desc: 'That address is the real printer — proving the guard also stops the gateway being aimed at the very devices it fronts.',
    test: `expectFail(400, 'file_url resolved to a disallowed address');` },

  { id: 'GW-URL-21', name: 'IPv6-mapped IPv4 loopback blocked → 400', only: ['strict', 'noAllowlist'],
    ...printJSON({ printer: '{{printer}}', file_url: 'http://[::ffff:127.0.0.1]/x.pdf' }),
    desc: 'One of the six IPv6 schemes that embed an IPv4 address; an IPv4-only blocklist would let all of them through.',
    test: `expectFail(400, 'file_url resolved to a disallowed address');` },

  { id: 'GW-URL-06', name: 'ftp:// scheme → 400',
    ...printJSON({ printer: '{{printer}}', file_url: 'ftp://example.com/a.pdf' }),
    test: `expectFail(400, 'file_url scheme must be http or https');` },

  { id: 'GW-URL-07', name: 'file:// scheme → 400',
    ...printJSON({ printer: '{{printer}}', file_url: 'file:///etc/passwd' }),
    test: `expectFail(400, 'file_url scheme must be http or https');` },

  { id: 'GW-URL-08', name: 'Embedded credentials → 400',
    ...printJSON({ printer: '{{printer}}', file_url: 'http://user:pass@example.com/a.pdf' }),
    test: `expectFail(400, 'file_url must not contain embedded credentials');` },

  { id: 'GW-URL-09', name: 'No host → 400',
    ...printJSON({ printer: '{{printer}}', file_url: 'http:///a.pdf' }),
    test: `expectFail(400, 'file_url must name a host');` },

  { id: 'GW-URL-10', name: 'Not a URL at all → 400',
    ...printJSON({ printer: '{{printer}}', file_url: 'not a url' }),
    desc: 'Rejected by the scheme check rather than by a parse failure: url.Parse is permissive and happily returns a relative reference with an empty scheme for this input, so "invalid file_url:" is NOT the message. Asserting the status plus whichever of the two gates fires keeps this honest without pinning it to the wrong one.',
    test: `expectFail(400);
           pm.test('rejected by one of the URL gates', function () {
             pm.expect(details()).to.match(/invalid file_url|scheme must be http or https/);
           });` },

  { id: 'GW-URL-19', name: 'Non-80/443 port: blocked strict, allowed permissive', only: ['noAllowlist'],
    ...printJSON({ printer: '{{printer}}', file_url: '{{fixtureBase}}/small.pdf' }),
    desc: 'One request, two profiles, opposite expectations — this is what proves ALLOW_PRIVATE_TARGETS lifts the PORT rule and not only the address rule. Run it under A and under E.',
    test: `if (flag('allowPrivate')) {
             if (!flag('fixturesUp')) { return skip('needs the fixture server'); }
             expectSubmitted();
           } else {
             expectFail(400, 'is not allowed (only 80/443)');
           }` },

  { id: 'GW-URL-11', name: 'Redirect not followed → 400', only: ['permissive', 'fixtures', 'noAllowlist'],
    ...printJSON({ printer: '{{printer}}', file_url: '{{fixtureBase}}/redirect' }),
    desc: 'A presigned URL is a direct link by construction; following a 3xx would re-open every address check on a server-chosen target.',
    test: `expectFail(400, 'file_url must be a direct link');` },

  { id: 'GW-URL-12', name: 'Upstream 404 → 502', only: ['permissive', 'fixtures', 'noAllowlist'],
    ...printJSON({ printer: '{{printer}}', file_url: '{{fixtureBase}}/notfound' }),
    test: `expectFail(502, 'file_url returned an error');
           expectNoLeak();
           pm.test('upstream body not reflected', function () {
             pm.expect(pm.response.text()).to.not.include('fixture-404-body');
           });` },

  { id: 'GW-URL-13', name: 'Nothing listening → 502', only: ['permissive', 'noAllowlist'],
    ...printJSON({ printer: '{{printer}}', file_url: 'http://127.0.0.1:1/x.pdf' }),
    desc: 'Under the strict guard this same URL is a 400 (port 1). Permissive is the only profile in which the dial itself is reached.',
    test: `expectFail(502, 'failed to download file_url');` },

  { id: 'GW-URL-16', name: 'Stalled host fails at the fetch timeout', only: ['permissive', 'fixtures', 'noAllowlist', 'defaultLimits'],
    ...printJSON({ printer: '{{printer}}', file_url: '{{fixtureBase}}/slow' }),
    desc: 'Profile E sets PRINT_GATEWAY_FETCH_TIMEOUT=5s. The point is that it fails in seconds, not at the 8-minute write timeout.',
    test: `expectAnyStatus([502, 504]);
           pm.test('failed fast (under 20s), not at the write timeout', function () {
             pm.expect(pm.response.responseTime).to.be.below(20000);
           });` },

  { id: 'GW-URL-17', name: 'Host not in the allowlist → 403', only: ['allowlist'],
    ...printJSON({ printer: '{{printer}}', file_url: '{{allowlistBlockedUrl}}' }),
    desc: '403, not 400 — the allowlist is a distinct gate from the address block, and conflating the two statuses would hide which one fired.',
    test: `expectFail(403, 'file_url host is not allowed');` },

  { id: 'GW-URL-18', name: 'Allowlisted host still address-checked → 400', only: ['allowlist', 'strict'],
    ...printJSON({ printer: '{{printer}}', file_url: 'http://localhost/a.pdf' }),
    desc: 'Profile F allowlists "localhost" precisely so this can be shown: passing the allowlist does not exempt a host from the address block.',
    test: `expectFail(400, 'file_url resolved to a disallowed address');` },

  { id: 'GW-URL-20', name: 'Permissive does not lift the allowlist → 403', only: ['permissive', 'allowlist'],
    ...printJSON({ printer: '{{printer}}', file_url: '{{fixtureBase}}/printDemo.pdf' }),
    desc: 'Profile E-allowlist only. The two knobs are independent: the address/port block is off, the host allowlist is still on.',
    test: `expectFail(403, 'file_url host is not allowed');` },

  { id: 'GW-URL-22', name: 'DNS rebinding → 400', only: ['manual'],
    ...printJSON({ printer: '{{printer}}', file_url: 'http://127.0.0.1.nip.io/x.pdf' }),
    desc: 'Needs a public hostname with an A record pointing at 127.0.0.1 (nip.io, if reachable). The property — the gate runs on the RESOLVED IP at connect(2), not on the submitted URL string — is the single most important one in the guard, so the row stays even when the environment cannot run it. Un-gate this scenario once name resolution for such a host is available.',
    test: `expectFail(400, 'file_url resolved to a disallowed address');` }
]);

/* ---------------- 05 s3_key intake ---------------- */
F('s3_key intake',
  'Option 3: a key in the one server-configured bucket. No SSRF surface by construction — the caller controls the key, never the destination. Prerequisite: scripts/setup-minio.sh has seeded the bucket (TEST-PLAN 5.3).',
[
  { id: 'GW-S3-01', name: 'Object storage disabled → 503', only: ['noS3'],
    ...printJSON({ printer: '{{printer}}', s3_key: '{{s3Key}}' }),
    desc: 'Absent S3 config is never a startup failure — it degrades this one capability and nothing else.',
    test: `expectFail(503, 'object storage is not configured');` },

  { id: 'GW-S3-02', name: 'Happy path → 200', only: ['healthyS3'],
    ...printJSON({ printer: '{{printer}}', s3_key: '{{s3Key}}' }),
    test: `expectSubmitted();` },

  { id: 'GW-S3-04', name: 'Missing key → 404', only: ['healthyS3'],
    ...printJSON({ printer: '{{printer}}', s3_key: '{{s3MissingKey}}' }),
    desc: 'A miss must be a 404, distinguishable from the 502 a broken store gives (GW-S3-08/09).',
    test: `expectFail(404, 'not found');` },

  { id: 'GW-S3-05', name: 'Path traversal key → 400', only: ['s3'],
    ...printJSON({ printer: '{{printer}}', s3_key: '../other-bucket/x.pdf' }),
    desc: 'What actually confines a caller to the one bucket. Rejected here regardless of backend, rather than relying on the store rejecting it (MinIO does; that is backend-specific).',
    test: `expectFail(400, 's3_key must not contain path traversal segments');` },

  { id: 'GW-S3-06', name: 'Traversal via ./.. segments → 400', only: ['s3'],
    ...printJSON({ printer: '{{printer}}', s3_key: 'a/./../../x.pdf' }),
    test: `expectFail(400, 's3_key must not contain path traversal segments');` },

  { id: 'GW-S3-10', name: 'Non-PDF object → 200 (documented gap)', only: ['healthyS3'],
    ...printJSON({ printer: '{{printer}}', s3_key: '{{s3TextKey}}' }),
    desc: 'ASSERTS A KNOWN GAP — the s3_key twin of GW-MP-10.',
    test: `expectSubmitted();` },

  { id: 'GW-S3-11', name: 'Empty object → 500 (lp refuses it)', only: ['healthyS3'],
    ...printJSON({ printer: '{{printer}}', s3_key: '{{s3EmptyKey}}' }),
    desc: 'The s3_key twin of GW-MP-11: downloaded and spooled without validation, then refused by lp. Same reasoning — see that row.',
    test: `expectFail(500, 'print submission failed');
           expectNoLeak();` },

  { id: 'GW-S3-12', name: 'Key with spaces and non-ASCII → 200', only: ['healthyS3'],
    ...printJSON({ printer: '{{printer}}', s3_key: '{{s3SpaceKey}}' }),
    desc: 'The key is used exactly as given — only traversal is rejected, not unusual characters.',
    test: `expectSubmitted();` }
]);

/* ---------------- 06 s3_key failure modes ---------------- */
F('s3_key failure modes',
  'Each row needs its own gateway configuration, so each runs under its own profile: scripts/profile.sh C-fault-endpoint | C-fault-creds | C-fault-slow. run-all.sh drives all three.',
[
  { id: 'GW-S3-08', name: 'Dead endpoint → 502, endpoint not disclosed', only: ['faultEndpoint'],
    ...printJSON({ printer: '{{printer}}', s3_key: '{{s3Key}}' }),
    test: `expectFail(502, 'failed to fetch object from storage');
           expectNoLeak();
           pm.test('endpoint address not disclosed', function () {
             pm.expect(pm.response.text()).to.not.include('127.0.0.1:9');
           });` },

  { id: 'GW-S3-09', name: 'Bad credentials → 502, not 404', only: ['faultCreds'],
    ...printJSON({ printer: '{{printer}}', s3_key: '{{s3Key}}' }),
    desc: 'An auth failure reported as a missing object would send whoever debugs it looking for the wrong problem entirely.',
    test: `expectFail(502, 'failed to fetch object from storage');
           pm.test('not misreported as a missing object', function () {
             pm.expect(pm.response.code).to.not.eql(404);
           });
           expectNoLeak();` },

  { id: 'GW-S3-13', name: 'Stalled store fails at the S3 timeout', only: ['faultSlow'],
    ...printJSON({ printer: '{{printer}}', s3_key: '{{s3Key}}' }),
    desc: 'Profile C-fault-slow points the S3 endpoint at the fixture server, which accepts and then stalls, with PRINT_GATEWAY_S3_TIMEOUT=2s.',
    test: `expectAnyStatus([502, 504]);
           pm.test('failed fast (under 20s), not at the write timeout', function () {
             pm.expect(pm.response.responseTime).to.be.below(20000);
           });` }
]);

/* ---------------- 07 Presign ---------------- */
F('Presign',
  'POST /files/presign mints a time-limited object-store URL — the one operation that hands out storage credentials by proxy. Same token as /print.',
[
  { id: 'GW-PRE-01', name: 'Storage disabled → 503', only: ['noS3'], path: '/files/presign',
    ...printJSON({ key: '{{s3Key}}' }),
    test: `expectFail(503, 'object storage is not configured');` },

  { id: 'GW-PRE-02', name: 'Default GET, default TTL', only: ['healthyS3'], path: '/files/presign',
    ...printJSON({ key: '{{s3Key}}' }),
    test: `expectPresigned(val('s3Key'));
           expectExpiry(Number(val('presignTtlSeconds')));` },

  { id: 'GW-PRE-03', name: 'Explicit method GET', only: ['healthyS3'], path: '/files/presign',
    ...printJSON({ key: '{{s3Key}}', method: 'GET' }),
    test: `expectPresigned(val('s3Key'));` },

  { id: 'GW-PRE-04', name: 'Method PUT', only: ['healthyS3'], path: '/files/presign',
    ...printJSON({ key: 'test/uploaded-by-presign.pdf', method: 'PUT' }),
    test: `expectPresigned('test/uploaded-by-presign.pdf');` },

  { id: 'GW-PRE-05', name: 'Lowercase method accepted', only: ['healthyS3'], path: '/files/presign',
    ...printJSON({ key: '{{s3Key}}', method: 'put' }),
    test: `expectPresigned(val('s3Key'));` },

  { id: 'GW-PRE-06', name: 'Method DELETE → 400', only: ['s3'], path: '/files/presign',
    ...printJSON({ key: '{{s3Key}}', method: 'DELETE' }),
    test: `expectFail(400, 'method must be GET or PUT');` },

  { id: 'GW-PRE-07', name: 'Shorter TTL honoured', only: ['healthyS3'], path: '/files/presign',
    ...printJSON({ key: '{{s3Key}}', ttl_seconds: 60 }),
    test: `expectStatus(200); expectExpiry(60, 30);` },

  { id: 'GW-PRE-08', name: 'Longer TTL clamped, not rejected', only: ['healthyS3'], path: '/files/presign',
    ...printJSON({ key: '{{s3Key}}', ttl_seconds: 86400 }),
    desc: 'A client asking for "as long as possible" is not a caller error — it is clamped down to the configured cap.',
    test: `expectStatus(200); expectExpiry(Number(val('presignTtlSeconds')));` },

  { id: 'GW-PRE-09', name: 'Absurd TTL clamped, never negative', only: ['healthyS3'], path: '/files/presign',
    ...printJSON({ key: '{{s3Key}}', ttl_seconds: 10000000000 }),
    desc: 'Guards the int64 overflow path: ttl_seconds * time.Second overflows well before this value, which would silently produce a NEGATIVE ttl (an already-expired URL) instead of the documented clamp. A plausible real input — milliseconds pasted where seconds were meant.',
    test: `expectStatus(200);
           pm.test('expiry is in the future, not overflowed negative', function () {
             pm.expect(Date.parse(pm.response.json().expires_at)).to.be.above(Date.now());
           });
           expectExpiry(Number(val('presignTtlSeconds')));` },

  { id: 'GW-PRE-10', name: 'Zero TTL → default', only: ['healthyS3'], path: '/files/presign',
    ...printJSON({ key: '{{s3Key}}', ttl_seconds: 0 }),
    test: `expectStatus(200); expectExpiry(Number(val('presignTtlSeconds')));` },

  { id: 'GW-PRE-11', name: 'Negative TTL → default', only: ['healthyS3'], path: '/files/presign',
    ...printJSON({ key: '{{s3Key}}', ttl_seconds: -5 }),
    test: `expectStatus(200); expectExpiry(Number(val('presignTtlSeconds')));` },

  { id: 'GW-PRE-12', name: 'Missing key → 400', only: ['s3'], path: '/files/presign',
    ...printJSON({}),
    test: `expectFail(400, 'key is required');` },

  { id: 'GW-PRE-13', name: 'Traversal key → 400', only: ['s3'], path: '/files/presign',
    ...printJSON({ key: '../x' }),
    test: `expectFail(400, 'key must not contain path traversal segments');` },

  { id: 'GW-PRE-14', name: 'Nonexistent key still presigns → 200', only: ['healthyS3'], path: '/files/presign',
    ...printJSON({ key: '{{s3MissingKey}}' }),
    desc: 'ASSERTS A DOCUMENTED BEHAVIOUR: presigning does not verify existence. The URL is minted and 404s at the store when used — see E2E-02b.',
    test: `expectPresigned(val('s3MissingKey'));` },

  { id: 'GW-PRE-15', name: 'Unknown JSON field → 400', only: ['s3'], path: '/files/presign',
    ...printJSON('{"key":"a","ttl":5}'),
    desc: '"ttl" instead of "ttl_seconds" — the strict decoder is what turns a silently-ignored typo into a visible mistake.',
    test: `expectFail(400, 'invalid JSON body');` },

  { id: 'GW-PRE-16', name: 'No token → 401', only: ['s3'], path: '/files/presign', auth: 'none',
    ...printJSON({ key: '{{s3Key}}' }),
    test: `expectFail(401, 'unauthorized');` },

  { id: 'GW-PRE-17', name: 'Presigned URL is never logged', only: ['manual'], path: '/files/presign',
    ...printJSON({ key: '{{s3Key}}' }),
    desc: 'The URL IS the credential. Asserted by scripts/ops-checks.sh, which greps journalctl for an X-Amz-Signature after issuing one — not observable from the response.',
    test: `expectStatus(200);` }
]);

/* ---------------- 08 Limits ---------------- */
F('Limits',
  'Every size gate, under profile G, whose tightened limits let the existing 2.5 MB printDemo.pdf and a few hundred bytes of JSON trip all of them. Prerequisite: scripts/profile.sh G.',
[
  { id: 'GW-MP-08', name: 'Oversize upload → 413', only: ['limits'], ...upload('{{printer}}', 'printDemo.pdf'),
    desc: '2.5 MB against profile G\'s 1 MiB cap. 413 rather than the generic 400 every other malformed-body case gets — the two are otherwise indistinguishable from the response alone.',
    test: `expectFail(413, 'request body exceeds the ' + val('maxUploadBytes') + ' byte limit');` },

  { id: 'GW-MP-09', name: 'Upload just under the limit → 200', only: ['limits'], ...upload('{{printer}}', 'small.pdf'),
    desc: 'The boundary\'s other side: the limit must not be off-by-one against a legitimate file.',
    test: `expectSubmitted();` },

  { id: 'GW-JSON-10', name: 'Oversize JSON body → 413', only: ['limits'],
    pre: `pm.variables.set('padding', 'p'.repeat(600));`,
    ...printJSON('{"printer":"{{printer}}","file_url":"https://example.com/{{padding}}.pdf"}'),
    desc: 'JSON intakes get a much tighter bound than uploads: a print-by-reference body has no legitimate reason to be large.',
    test: `expectFail(413, 'request body exceeds the ' + val('maxJsonBytes') + ' byte limit');` },

  { id: 'GW-URL-14', name: 'Oversize fetch, honest Content-Length → 413', only: ['limits', 'fixtures', 'noAllowlist'],
    ...printJSON({ printer: '{{printer}}', file_url: '{{fixtureBase}}/printDemo.pdf' }),
    test: `expectFail(413, 'file_url response is too large');` },

  { id: 'GW-URL-15', name: 'Oversize fetch, chunked and lying → 413', only: ['limits', 'fixtures', 'noAllowlist'],
    ...printJSON({ printer: '{{printer}}', file_url: '{{fixtureBase}}/lying-chunked' }),
    desc: 'No Content-Length at all, so only the LimitReader around the body can catch it. This is the row that proves the up-front check is not the only defence.',
    test: `expectFail(413, 'file_url response is too large');` },

  { id: 'GW-S3-07', name: 'Oversize object → 413', only: ['limits', 'healthyS3'],
    ...printJSON({ printer: '{{printer}}', s3_key: '{{s3LargeKey}}' }),
    desc: 'Checked against the store\'s own authoritative size metadata before a single byte is copied — unlike file_url\'s Content-Length, which is a claim until the read catches the lie.',
    test: `expectFail(413, 'object exceeds the maximum allowed size of ' + val('s3MaxBytes') + ' bytes');` }
]);

/* ---------------- 09 End-to-end chains ---------------- */
F('End-to-end chains',
  'Multi-step flows that cross features. Requests here depend on the one before them (they pass state through collection variables), so run the folder in order — do not cherry-pick a single request out of it.',
[
  { id: 'E2E-01a', name: 'Presign a PUT URL', only: ['healthyS3'], path: '/files/presign',
    ...printJSON({ key: 'test/e2e-upload.pdf', method: 'PUT', ttl_seconds: 300 }),
    desc: 'Step 1 of the recommended large-file path: no document bytes ever cross the gateway on intake.',
    test: `expectPresigned('test/e2e-upload.pdf');
           pm.collectionVariables.set('e2ePutUrl', pm.response.json().url);` },

  { id: 'E2E-01b', name: 'Upload straight to the store', only: ['healthyS3'], external: true,
    externalUrl: '{{e2ePutUrl}}', method: 'PUT', auth: 'none',
    formdata: undefined,
    headers: [{ key: 'Content-Type', value: 'application/pdf' }],
    raw: 'E2E-PLACEHOLDER-BODY',
    desc: 'Step 2. Newman cannot attach a file to a raw PUT from a collection variable URL, so this uploads a small marker body — enough to prove the presigned PUT is honoured by the store. The PDF-bytes version of this chain is scripts/ops-checks.sh e2e-upload.',
    test: `expectAnyStatus([200, 204]);` },

  { id: 'E2E-01c', name: 'Print the uploaded object by key', only: ['healthyS3'],
    ...printJSON({ printer: '{{printer}}', s3_key: 'test/e2e-upload.pdf' }),
    desc: 'Step 3: the key written in E2E-01b is now printable. Completes the presign→upload→print chain.',
    test: `expectSubmitted();` },

  { id: 'E2E-02a', name: 'Presign a GET URL', only: ['healthyS3'], path: '/files/presign',
    ...printJSON({ key: '{{s3Key}}', method: 'GET', ttl_seconds: 300 }),
    test: `expectPresigned(val('s3Key'));
           pm.collectionVariables.set('e2eGetUrl', pm.response.json().url);` },

  { id: 'E2E-02b', name: 'Fetch it directly from the store', only: ['healthyS3'], external: true,
    externalUrl: '{{e2eGetUrl}}', method: 'GET', auth: 'none',
    desc: 'Proves the minted URL actually works against the backend, without our credentials.',
    test: `expectStatus(200);
           pm.test('got the seeded document back', function () {
             // Deliberately NOT a byte-count threshold: profile G points s3Key
             // at small.pdf (698 B) so the general S3 rows do not trip its
             // tightened cap, and a '> 1000' assertion here then failed for a
             // reason having nothing to do with presigning. PDF magic bytes
             // plus non-emptiness hold under every profile.
             pm.expect(pm.response.responseSize, 'empty body').to.be.above(0);
             pm.expect(pm.response.text().slice(0, 5), 'PDF magic bytes').to.eql('%PDF-');
           });` },

  { id: 'E2E-04', name: 'AWS presigned GET printed via file_url, strict guard', only: ['healthyS3', 'backendAws', 'strict'],
    ...printJSON({ printer: '{{printer}}', file_url: '{{e2eGetUrl}}' }),
    desc: 'The ONLY file_url happy path that runs with the production security posture intact: an AWS presigned URL is public HTTPS on 443, so it passes the strict guard unaided. Depends on E2E-02a having run in this same run.',
    test: `expectSubmitted();` },

  { id: 'E2E-05', name: 'Local presigned GET via file_url is blocked', only: ['healthyS3', 'backendLocal', 'strict'],
    ...printJSON({ printer: '{{printer}}', file_url: '{{e2eGetUrl}}' }),
    desc: 'The mirror of E2E-04, and the reason profile E exists: the same chain against local MinIO is correctly refused, because the URL is loopback on a non-443 port. A 200 here would mean the guard had been disabled.',
    test: `expectFail(400);
           pm.test('refused by the guard, not by the store', function () {
             pm.expect(details()).to.match(/disallowed address|not allowed \\(only 80\\/443\\)/);
           });` },

  { id: 'E2E-06', name: 'Expired presigned URL is refused', only: ['manual'], path: '/files/presign',
    ...printJSON({ key: '{{s3Key}}', ttl_seconds: 5 }),
    desc: 'Needs a real wait between minting and using the URL, which the Postman sandbox has no way to do. Implemented in scripts/ops-checks.sh expired-presign.',
    test: `expectStatus(200);` }
]);

/* ---------------- 10 Real paper ---------------- */
F('Real paper',
  'PHYSICALLY PRINTS on the Brother MFC-L2700DW via the brother-direct queue. Excluded unless run-newman.sh is given --with-paper. A human must confirm the output; the assertions here only prove submission succeeded.',
[
  { id: 'GW-MP-02', name: 'Multipart to the real printer → 2 pages', only: ['paper'],
    ...upload('{{paperPrinter}}', 'printDemo.pdf'),
    headers: [{ key: 'X-Laas-Identifier', value: 'paper-mp-{{$timestamp}}' }],
    desc: 'CONFIRM AT THE PRINTER: 2 pages. Prerequisite: brother-direct enabled, printer online and loaded.',
    test: `expectSubmitted();
           pm.test('submitted to the paper queue', function () {
             pm.expect(pm.response.json().output).to.include(val('paperPrinter'));
           });` },

  { id: 'GW-S3-03', name: 's3_key to the real printer → 2 pages', only: ['paper', 'healthyS3'],
    ...printJSON({ printer: '{{paperPrinter}}', s3_key: '{{s3Key}}' }),
    headers: [{ key: 'X-Laas-Identifier', value: 'paper-s3-{{$timestamp}}' }],
    desc: 'CONFIRM AT THE PRINTER: 2 pages.',
    test: `expectSubmitted();` },

  { id: 'E2E-03', name: 'presign→upload→print to the real printer', only: ['paper', 'healthyS3'],
    ...printJSON({ printer: '{{paperPrinter}}', s3_key: '{{s3Key}}' }),
    headers: [{ key: 'X-Laas-Identifier', value: 'paper-e2e-{{$timestamp}}' }],
    desc: 'CONFIRM AT THE PRINTER: 2 pages. Run the End-to-end chains folder first so the object exists.',
    test: `expectSubmitted();` }
]);

/* ---------------- 11 Vault precedence ---------------- */
F('Vault precedence',
  'The HTTP-observable half of the Vault contract. The process-level half (refuse-to-start, fallback logging) is scripts/startup-matrix.sh — it cannot be asserted from a client, because a failing case has no listener at all.',
[
  { id: 'GW-SEC-02a', name: 'Environment token is rejected when Vault supplies one', only: ['vault'], auth: 'alt',
    desc: 'Profile B/D deliberately set PRINT_GATEWAY_TOKEN to a different value. If this 401 ever became a 200, Vault silently stopped being the source of truth — the exact regression this row exists to catch.',
    test: `expectFail(401, 'unauthorized');` },

  { id: 'GW-SEC-02b', name: 'Vault token authenticates', only: ['vault'], ctype: 'text/plain', raw: 'x',
    desc: '415 (not 401) proves the Vault-sourced token was accepted.',
    test: `expectFail(415, 'Content-Type must be');
           pm.test('authenticated', function () { pm.expect(pm.response.code).to.not.eql(401); });` }
]);

/* ================================================================== *
 * Collection assembly
 * ================================================================== */

const collection = {
  info: {
    _postman_id: 'b7d1f0c2-4a3e-4f21-9c8d-printgateway0001',
    name: 'LAB-16894 Print Gateway',
    description: [
      'GENERATED FILE — do not edit. Edit tests/postman/build-collection.js and re-run:',
      '',
      '    node tests/postman/build-collection.js',
      '',
      'Every request name starts with its scenario ID from tests/TEST-PLAN.md.',
      '',
      'Run headless, per profile:',
      '    tests/scripts/profile.sh A && tests/scripts/run-newman.sh A',
      '',
      'A scenario whose profile does not provide what it needs reports as SKIPPED,',
      'not as a failure — so the same collection runs green against every profile',
      'and each profile simply exercises a different subset.',
      '',
      'ASSERTION RULE: nothing here asserts on errorCode or errorMessage.',
      'error_handler hardcodes both on every failure regardless of status, so only',
      'the HTTP status and errorDetails.details discriminate (TEST-PLAN section 3).'
    ].join('\n'),
    schema: 'https://schema.getpostman.com/json/collection/v2.1.0/collection.json'
  },
  item: folders,
  event: [
    {
      listen: 'test',
      script: {
        type: 'text/javascript',
        exec: [
          "// Runs after EVERY request in the collection.",
          "// Guarded to gateway-addressed requests: the End-to-end folder also talks",
          "// straight to the object store, which has no reason to carry our headers.",
          "var base = String(pm.variables.get('baseUrl') || '');",
          "if (base && pm.request.url.toString().indexOf(base) === 0) {",
          "  pm.test('[all] X-Laas-Identifier present', function () {",
          "    pm.expect(pm.response.headers.has('X-Laas-Identifier'), 'every response carries a correlation id').to.be.true;",
          "  });",
          "  if (pm.response.code >= 400 && pm.response.code !== 404) {",
          "    pm.test('[all] labOS error envelope', function () {",
          "      var j = pm.response.json();",
          "      pm.expect(j).to.have.property('errorCode');",
          "      pm.expect(j).to.have.property('errorMessage');",
          "      pm.expect(j).to.have.nested.property('errorDetails.details');",
          "    });",
          "  }",
          "}"
        ]
      }
    }
  ],
  variable: [
    { key: 'utils', value: UTILS, type: 'string' },
    { key: 'e2ePutUrl', value: '', type: 'string' },
    { key: 'e2eGetUrl', value: '', type: 'string' }
  ]
};

/* ================================================================== *
 * Environments
 * ================================================================== */

function resolvePlaceholders(value, vars, missing, where) {
  return String(value).replace(/\$\{([A-Z0-9_]+)\}/g, (_, name) => {
    if (vars[name] === undefined || vars[name] === '') {
      // Recorded, not thrown: the Postman environment is generated on a dev box
      // that may legitimately have no AWS keys. profile.sh is the component that
      // refuses to switch to a profile whose backend is not configured; this
      // only needs to leave the variable visibly empty.
      missing.push(`${where}: \${${name}}`);
      return '';
    }
    return vars[name];
  });
}

function loadEnvLocal() {
  const p = path.join(TESTS_DIR, '.env.local');
  if (!fs.existsSync(p)) return {};
  const out = {};
  fs.readFileSync(p, 'utf8').split('\n').forEach(line => {
    const m = line.match(/^\s*(?:export\s+)?([A-Z0-9_]+)\s*=\s*(.*)\s*$/);
    if (m && !line.trim().startsWith('#')) out[m[1]] = m[2].replace(/^["']|["']$/g, '');
  });
  return out;
}

function buildEnvironments() {
  const vars = Object.assign({}, PROFILES.vars, loadEnvLocal(), process.env);
  const missing = [];
  fs.mkdirSync(OUT_ENV_DIR, { recursive: true });

  const written = [];
  for (const [name, prof] of Object.entries(PROFILES.profiles)) {
    const merged = Object.assign({}, PROFILES.postmanDefaults, PROFILES.objectKeys, prof.postman || {});
    const values = Object.entries(merged).map(([k, v]) => ({
      key: k,
      value: resolvePlaceholders(v, vars, missing, `${name}.${k}`),
      type: k === 'token' || k === 'altToken' ? 'secret' : 'default',
      enabled: true
    }));
    // s3Backend is injected at run time by run-newman.sh (--env-var), since one
    // profile is run against three different backends.
    values.push({ key: 's3Backend', value: 'local', type: 'default', enabled: true });

    const env = {
      id: `printgw-env-${name.toLowerCase()}`,
      name: `printgw ${name} (${prof.label})`,
      values,
      _postman_variable_scope: 'environment'
    };
    const file = path.join(OUT_ENV_DIR, `${name}.postman_environment.json`);
    fs.writeFileSync(file, JSON.stringify(env, null, 2) + '\n');
    written.push(path.basename(file));
  }
  return { written, missing };
}

/* ================================================================== *
 * Plan-coverage check
 * ================================================================== */

function checkPlanCoverage(ids) {
  const plan = fs.readFileSync(path.join(TESTS_DIR, 'TEST-PLAN.md'), 'utf8');
  // Scenario ids the plan declares as driven by something other than Postman.
  const nonHttp = /^(IPP|FIX)-/;
  const scriptDriven = new Set([
    'GW-SEC-01', 'GW-SEC-03', 'GW-SEC-04', 'GW-SEC-05', 'GW-SEC-06', 'GW-SEC-07',
    'GW-SEC-08', 'GW-SEC-09', 'GW-SEC-10', 'GW-SEC-11', 'GW-SEC-12', 'GW-SEC-13',
    'GW-SEC-14', 'GW-SEC-15', 'GW-SEC-16', 'GW-SEC-02',
    'GW-OPS-01', 'GW-OPS-02', 'GW-OPS-03', 'GW-OPS-04', 'GW-OPS-05', 'GW-OPS-06',
    'GW-OPS-07', 'GW-OPS-08', 'GW-OPS-09', 'GW-OPS-10',
    'E2E-07', 'E2E-08'
  ]);
  const planIds = new Set();
  const re = /\b((?:GW-[A-Z0-9]+|IPP|IPP-BENCH|FIX|E2E)-\d+)\b/g;
  let m;
  while ((m = re.exec(plan)) !== null) planIds.add(m[1]);
  // GW-CFG-* are startup-matrix rows by definition.
  for (const id of planIds) if (id.startsWith('GW-CFG-')) scriptDriven.add(id);

  const have = new Set(ids.map(i => i.replace(/[a-c]$/, '')));
  const uncovered = [...planIds].filter(id =>
    !nonHttp.test(id) && !scriptDriven.has(id) && !have.has(id) && !ids.includes(id));
  const orphans = ids.filter(id => !planIds.has(id) && !planIds.has(id.replace(/[a-c]$/, '')));
  return { uncovered: uncovered.sort(), orphans: orphans.sort() };
}

/* ================================================================== *
 * Emit
 * ================================================================== */

const ids = [];
folders.forEach(f => f.item.forEach(i => ids.push(i.name.split(' · ')[0])));

const dupes = ids.filter((id, i) => ids.indexOf(id) !== i);
if (dupes.length) {
  console.error('FAIL: duplicate scenario ids: ' + [...new Set(dupes)].join(', '));
  process.exit(1);
}

fs.writeFileSync(OUT_COLLECTION, JSON.stringify(collection, null, 2) + '\n');
const { written, missing } = buildEnvironments();
const { uncovered, orphans } = checkPlanCoverage(ids);

console.log(`collection : ${path.relative(TESTS_DIR, OUT_COLLECTION)}  (${folders.length} folders, ${ids.length} requests)`);
folders.forEach(f => console.log(`             ${String(f.item.length).padStart(3)}  ${f.name}`));
console.log(`environments: ${written.join(', ')}`);

if (orphans.length) {
  console.error('\nFAIL: requests with no matching row in TEST-PLAN.md:\n  ' + orphans.join('\n  '));
  process.exit(1);
}
if (uncovered.length) {
  console.warn('\nNOTE: plan rows not covered by this collection (expected only if script-driven):\n  ' + uncovered.join('\n  '));
}
if (missing.length) {
  console.warn('\nNOTE: unresolved placeholders (those profiles will not run until tests/.env.local supplies them):');
  [...new Set(missing)].forEach(x => console.warn('  ' + x));
}
