import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { calibrationVariance, chooseAllocation, buildReport, tQuantile, upperBound } from '../analyze.mjs';

const protocol = JSON.parse(await readFile(new URL('../protocol.json', import.meta.url), 'utf8'));
const identity = { campaign_id: 'synthetic', action: 'run', engine_sha: 'engine', harness_sha: 'harness', suite_hash: 'suite', n_tasks: 12, repetitions: 1, n_pairs: 12, n_on: 12, n_off: 12, platform: process.platform };
const classes = ['correctness_repair', 'feature_extension', 'behavior_preserving_refactor', 'failure_path_repair'];
function row(task, repetition, arm, ratio, overrides = {}) {
  const total = arm === 'on' ? 100 * ratio : 100;
  return { ...identity, ...overrides, task_id: task, task_class: classes[Number(task.slice(1)) % 4], repetition, arm, attempt_status: overrides.attempt_status ?? 'completed', passed: overrides.passed ?? true, elapsed_ms: 1000, verified_wall_time_ms: overrides.verified_wall_time_ms ?? (arm === 'on' ? 800 : 1000), usage: { main: { input_tokens: total, cache_read_input_tokens: 0, cache_creation_input_tokens: 0, output_tokens: 0 }, aggregate: { input_tokens: total, cache_read_input_tokens: 0, cache_creation_input_tokens: 0, output_tokens: 0 }, reconciliation_mismatch: false, workers: 'unknown' }, accounting_error: undefined };
}
function confirmation() {
  const records = [];
  for (let i = 0; i < 12; i++) {
    const task = `t${String(i).padStart(2, '0')}`;
    records.push(row(task, 1, 'off', 1), row(task, 1, 'on', 0.5, i === 0 ? { passed: false, attempt_status: 'failed', verified_wall_time_ms: 5000 } : {}));
  }
  return records;
}

test('allocation uses calibration task clusters and chooses a feasible balanced K and R', () => {
  const records = [];
  for (let i = 0; i < 8; i++) for (let rep = 1; rep <= 2; rep++) {
    const task = `c${i}`;
    const d = -0.4 + i * 0.1 + (rep === 1 ? -0.1 : 0.1);
    records.push({ ...row(task, rep, 'off', 1), ...identity, n_tasks: 8, n_pairs: 16, n_on: 16, n_off: 16, repetitions: 2, task_class: classes[i % 4] });
    records.push({ ...row(task, rep, 'on', Math.exp(d)), ...identity, n_tasks: 8, n_pairs: 16, n_on: 16, n_off: 16, repetitions: 2, task_class: classes[i % 4] });
  }
  const components = calibrationVariance(records, { repetitions: 2 });
  assert.ok(components.main_claude_tokens.tau_squared_between_task > 0.01);
  assert.ok(components.main_claude_tokens.sigma_squared_within_task > 0);
  assert.equal(components.main_claude_tokens.task_count, 8);
  const allocation = chooseAllocation(components);
  assert.ok(allocation.tasks >= protocol.k_floor && allocation.tasks % 4 === 0);
  assert.ok(allocation.repetitions >= 1);
  for (const endpoint of Object.values(allocation.achieved_power)) assert.ok(endpoint.power >= 0.89);
});

test('task-level paired means average repetitions before estimating task variance', () => {
  const records = [];
  for (let i = 0; i < 8; i++) for (let rep = 1; rep <= 2; rep++) {
    const task = `c${i}`, d = i * 0.1 + (rep === 1 ? -0.2 : 0.2);
    const off = row(task, rep, 'off', 1), on = row(task, rep, 'on', Math.exp(d));
    for (const r of [off, on]) Object.assign(r, { n_tasks: 8, repetitions: 2, n_pairs: 16, n_on: 16, n_off: 16, task_class: classes[i % 4] });
    records.push(off, on);
  }
  const components = calibrationVariance(records, { repetitions: 2 });
  assert.ok(Math.abs(components.main_claude_tokens.sigma_squared_within_task - 0.08) < 1e-7);
  assert.ok(Math.abs(components.main_claude_tokens.tau_squared_between_task - 0.02) < 1e-7);
});

test('one-sided bound uses the upper log-ratio direction for savings', () => {
  const favorable = upperBound(Array(12).fill(Math.log(0.5)));
  const unfavorable = upperBound(Array(12).fill(Math.log(1.2)));
  assert.ok(favorable.savings_lower_bound > 0);
  assert.ok(unfavorable.savings_lower_bound < 0);
  assert.equal(tQuantile(0.975, 11) > 2.2, true);
});

test('upper bound refuses fewer than two task values', () => {
  for (const values of [[], [Math.log(0.5)]]) assert.throws(() => upperBound(values), /at least 2 task values/);
});

test('confirmation reports refuse task counts below the protocol floor', () => {
  for (const count of [1, protocol.k_floor - 1]) {
    const metadata = { ...identity, n_tasks: count, n_pairs: count, n_on: count, n_off: count };
    const records = confirmation().slice(0, count * 2).map(record => ({ ...record, ...metadata }));
    assert.throws(() => buildReport(metadata, records, 'synthetic'), new RegExp(`at least ${protocol.k_floor} tasks`));
  }
});

test('zero-variance allocation still respects the protocol task floor', () => {
  const components = Object.fromEntries(protocol.endpoints.map(endpoint => [endpoint.id, { tau_squared_between_task: 0, sigma_squared_within_task: 0 }]));
  assert.ok(chooseAllocation(components).tasks >= protocol.k_floor);
});

test('allocation ignores descriptive endpoint variance', () => {
  const components = {
    main_claude_tokens: { tau_squared_between_task: 0, sigma_squared_within_task: 0.110 },
    verified_wall_time: { tau_squared_between_task: 0, sigma_squared_within_task: 3.076 }
  };
  const allocation = chooseAllocation(components);
  assert.equal(allocation.tasks, protocol.k_floor);
  assert.equal(allocation.repetitions, 1);
  assert.deepEqual(Object.keys(allocation.achieved_power), ['main_claude_tokens']);
});

test('confirmatory claims exclude descriptive endpoints and report descriptive intervals', () => {
  const records = confirmation().map(record => ({ ...record, passed: true, attempt_status: 'completed', verified_wall_time_ms: 1000 }));
  const report = buildReport(identity, records, 'synthetic');
  assert.deepEqual(Object.keys(report.result.claims), ['main_claude_tokens']);
  assert.deepEqual(Object.keys(report.result.descriptive_endpoints), ['verified_wall_time']);
  const tokens = report.result.claims.main_claude_tokens, time = report.result.descriptive_endpoints.verified_wall_time;
  assert.ok(tokens.upper_log_ratio < 0);
  assert.equal(tokens.decision, 'supported');
  assert.equal(time.mean_log_ratio, 0);
  assert.deepEqual(time.descriptive_95_percent_interval, { lower_log_ratio: 0, upper_log_ratio: 0 });
  assert.match(report.markdown, /Main-Claude tokens[^\n]*\| supported \|/);
  assert.match(report.markdown, /Descriptive endpoint estimates \(no confirmatory claims; 95% intervals\)/);
  assert.match(report.markdown, /Wall time to a verified success[^\n]*\| 0\.000000 \| \[0\.000000, 0\.000000\] \|/);
});

test('failed attempts remain in endpoint pairs and every pass-rate denominator', () => {
  const report = buildReport(identity, confirmation(), 'synthetic');
  assert.equal(report.result.claims.main_claude_tokens.tasks, 12);
  assert.equal(report.result.descriptive_endpoints.verified_wall_time.tasks, 12);
  assert.equal(report.result.descriptive_endpoints.verified_wall_time.mean_log_ratio, Number((Math.log(0.8) * 11 / 12 + Math.log(5) / 12).toFixed(8)));
  assert.deepEqual(report.result.descriptive_pass_rates.all.on, { passed: 11, attempted: 12, pass_rate: Number((11 / 12).toFixed(8)) });
  assert.deepEqual(report.result.descriptive_pass_rates.all.off, { passed: 12, attempted: 12, pass_rate: 1 });
  assert.equal(report.result.descriptive_pass_rates[classes[0]].on.attempted, 3);
});

test('report refuses invalid wall times and non-finite computed statistics', () => {
  for (const passed of [true, false]) for (const time of [0, -1, Infinity, NaN, undefined]) {
    const records = confirmation();
    Object.assign(records[0], { passed, verified_wall_time_ms: time });
    assert.throws(() => buildReport(identity, records, 'synthetic'), /positive finite value/);
  }
  const records = confirmation();
  records[0].verified_wall_time_ms = Number.MIN_VALUE;
  records[1].verified_wall_time_ms = Number.MAX_VALUE;
  assert.throws(() => buildReport(identity, records, 'synthetic'), /non-finite report statistic/);
});

test('report refuses missing or mixed provenance and unknown accounting', () => {
  const records = confirmation();
  assert.throws(() => buildReport(identity, records.map(({ suite_hash, ...r }) => r), 'synthetic'), /provenance/);
  const mixed = confirmation(); mixed[0].engine_sha = 'other';
  assert.throws(() => buildReport(identity, mixed, 'synthetic'), /provenance/);
  const unknown = confirmation(); unknown[0].usage.aggregate = 'unknown';
  assert.throws(() => buildReport(identity, unknown, 'synthetic'), /accounting/);
});

test('same report input produces byte-identical Markdown and JSON', () => {
  const records = confirmation(), first = buildReport(identity, records, 'synthetic'), second = buildReport(identity, records, 'synthetic');
  assert.equal(first.markdown, second.markdown);
  assert.equal(first.jsonText, second.jsonText);
  assert.match(first.markdown, /n_tasks=12; repetitions=1; n_pairs=12; n_on=12; n_off=12/);
});

test('seeded small-sample one-sided t intervals have near-nominal coverage', () => {
  let seed = 19427;
  const random = () => { seed = (1664525 * seed + 1013904223) >>> 0; return (seed + 1) / 4294967297; };
  const normal = () => { const u = random(), v = random(); return Math.sqrt(-2 * Math.log(u)) * Math.cos(2 * Math.PI * v); };
  let covered = 0, reps = 4000;
  for (let rep = 0; rep < reps; rep++) {
    const sample = Array.from({ length: 12 }, normal);
    if (upperBound(sample).upper_log_ratio >= 0) covered++;
  }
  const coverage = covered / reps;
  assert.ok(coverage > 0.96 && coverage < 0.99, `coverage=${coverage}`);
});
