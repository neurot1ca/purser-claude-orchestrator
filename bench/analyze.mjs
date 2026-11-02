import { readFile, writeFile, mkdir } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { retryIO } from './run.mjs';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const PROTOCOL = JSON.parse(await readFile(path.join(ROOT, 'bench', 'protocol.json'), 'utf8'));
const CATEGORIES = PROTOCOL.endpoints[0].token_categories;
const PROVENANCE = PROTOCOL.provenance_fields;
const CLASSES = PROTOCOL.task_classes;
const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
const mean = xs => xs.reduce((a, b) => a + b, 0) / xs.length;
const variance = xs => xs.length < 2 ? 0 : xs.reduce((s, x) => s + (x - mean(xs)) ** 2, 0) / (xs.length - 1);
const round = n => Number(n.toFixed(8));
const fmt = n => Number(n).toFixed(6);

function logGamma(z) {
  const c = [676.5203681218851, -1259.1392167224028, 771.3234287776531, -176.6150291621406, 12.507343278686905, -0.13857109526572012, 9.984369578019572e-6, 1.5056327351493116e-7];
  if (z < 0.5) return Math.log(Math.PI) - Math.log(Math.sin(Math.PI * z)) - logGamma(1 - z);
  z -= 1;
  let x = 0.9999999999998099;
  for (let i = 0; i < c.length; i++) x += c[i] / (z + i + 1);
  const t = z + c.length - 0.5;
  return 0.5 * Math.log(2 * Math.PI) + (z + 0.5) * Math.log(t) - t + Math.log(x);
}
function betaFraction(a, b, x) {
  const qab = a + b, qap = a + 1, qam = a - 1, tiny = 1e-300;
  let c = 1, d = 1 - qab * x / qap;
  if (Math.abs(d) < tiny) d = tiny;
  d = 1 / d;
  let h = d;
  for (let m = 1; m <= 200; m++) {
    const m2 = 2 * m;
    let aa = m * (b - m) * x / ((qam + m2) * (a + m2));
    d = 1 + aa * d; if (Math.abs(d) < tiny) d = tiny;
    c = 1 + aa / c; if (Math.abs(c) < tiny) c = tiny;
    d = 1 / d; h *= d * c;
    aa = -(a + m) * (qab + m) * x / ((a + m2) * (qap + m2));
    d = 1 + aa * d; if (Math.abs(d) < tiny) d = tiny;
    c = 1 + aa / c; if (Math.abs(c) < tiny) c = tiny;
    d = 1 / d;
    const delta = d * c; h *= delta;
    if (Math.abs(delta - 1) < 3e-14) break;
  }
  return h;
}
function regularizedBeta(x, a, b) {
  if (x <= 0) return 0;
  if (x >= 1) return 1;
  const front = Math.exp(logGamma(a + b) - logGamma(a) - logGamma(b) + a * Math.log(x) + b * Math.log1p(-x));
  return x < (a + 1) / (a + b + 2) ? front * betaFraction(a, b, x) / a : 1 - front * betaFraction(b, a, 1 - x) / b;
}
function tCDF(t, df) {
  const x = df / (df + t * t), tail = 0.5 * regularizedBeta(x, df / 2, 0.5);
  return t >= 0 ? 1 - tail : tail;
}
export function tQuantile(p, df) {
  let lo = -100, hi = 100;
  for (let i = 0; i < 100; i++) { const mid = (lo + hi) / 2; if (tCDF(mid, df) < p) lo = mid; else hi = mid; }
  return (lo + hi) / 2;
}
function normalCDF(x) {
  const sign = x < 0 ? -1 : 1, a = Math.abs(x), t = 1 / (1 + 0.2316419 * a);
  const density = 0.3989422804014327 * Math.exp(-a * a / 2);
  const tail = density * t * (0.319381530 + t * (-0.356563782 + t * (1.781477937 + t * (-1.821255978 + t * 1.330274429))));
  return sign > 0 ? 1 - tail : tail;
}

function groups(records) {
  const map = new Map();
  for (const row of records) {
    if (!['on', 'off'].includes(row.arm) || !row.task_id || !Number.isInteger(row.repetition)) throw new Error('invalid attempt identity');
    const key = `${row.task_id}\0${row.repetition}`;
    if (!map.has(key)) map.set(key, { task_id: row.task_id, repetition: row.repetition, arms: {} });
    const pair = map.get(key);
    if (pair.arms[row.arm]) throw new Error(`duplicate ${row.arm} attempt for ${row.task_id} repetition ${row.repetition}`);
    pair.arms[row.arm] = row;
  }
  return [...map.values()].sort((a, b) => (a.task_id < b.task_id ? -1 : a.task_id > b.task_id ? 1 : 0) || a.repetition - b.repetition);
}
function taskRows(pairs) {
  const tasks = new Map();
  for (const pair of pairs) {
    if (!pair.arms.on || !pair.arms.off) throw new Error(`incomplete pair for ${pair.task_id} repetition ${pair.repetition}`);
    const on = pair.arms.on, off = pair.arms.off;
    if (on.task_class !== off.task_class) throw new Error(`task class mismatch for ${pair.task_id}`);
    if (!tasks.has(pair.task_id)) tasks.set(pair.task_id, { task_id: pair.task_id, task_class: on.task_class, pairs: [] });
    const task = tasks.get(pair.task_id);
    if (task.task_class !== on.task_class) throw new Error(`task class mismatch for ${pair.task_id}`);
    task.pairs.push(pair);
  }
  return [...tasks.values()].sort((a, b) => a.task_id < b.task_id ? -1 : a.task_id > b.task_id ? 1 : 0);
}
function endpointValue(row, endpoint) {
  if (endpoint === 'main_claude_tokens') {
    const usage = row.usage;
    if (!usage || !usage.main || usage.main === 'unknown' || usage.aggregate === 'unknown' || usage.reconciliation_mismatch !== false || row.accounting_error) throw new Error(`unknown or unreconciled token accounting for ${row.task_id}/${row.repetition}/${row.arm}`);
    const values = CATEGORIES.map(key => usage.main[key]);
    if (values.some(x => !Number.isFinite(x) || x < 0)) throw new Error(`incomplete token accounting for ${row.task_id}/${row.repetition}/${row.arm}`);
    const total = values.reduce((a, b) => a + b, 0);
    if (total <= 0) throw new Error(`zero token total for ${row.task_id}/${row.repetition}/${row.arm}`);
    return total;
  }
  const time = row.verified_wall_time_ms;
  if (!Number.isFinite(time) || time <= 0) throw new Error(`invalid verified wall time for ${row.task_id}/${row.repetition}/${row.arm}: expected a positive finite value`);
  return time;
}
function pairedTaskLogMeans(tasks, endpoint) {
  return tasks.map(task => mean(task.pairs.map(pair => {
    const yOn = endpointValue(pair.arms.on, endpoint), yOff = endpointValue(pair.arms.off, endpoint);
    return Math.log(yOn / yOff);
  })));
}
export function upperBound(values, alpha = PROTOCOL.alpha) {
  if (values.length < 2) throw new Error('upper bound requires at least 2 task values');
  // Tasks are the independent units; the Student t bound accounts for the small task count.
  const avg = mean(values), sd = Math.sqrt(variance(values)), k = values.length;
  const upper = avg + tQuantile(1 - alpha, k - 1) * sd / Math.sqrt(k);
  return { mean_log_ratio: round(avg), task_sd: round(sd), upper_log_ratio: round(upper), savings_lower_bound: round(1 - Math.exp(upper)), tasks: k };
}

export function calibrationVariance(records, metadata) {
  const pairs = groups(records), tasks = taskRows(pairs);
  if (tasks.length !== PROTOCOL.calibration_size.tasks || metadata.repetitions !== PROTOCOL.calibration_size.paired_repetitions || pairs.length !== PROTOCOL.calibration_size.total_pairs) throw new Error('calibration campaign must contain the protocol task and repetition counts');
  if (tasks.some(task => task.pairs.length !== metadata.repetitions || task.pairs.some((pair, i) => pair.repetition !== i + 1))) throw new Error('calibration task repetitions do not match campaign metadata');
  const endpoints = {};
  for (const endpoint of PROTOCOL.endpoints) {
    const taskMeans = pairedTaskLogMeans(tasks, endpoint.id);
    const within = [];
    for (const task of tasks) {
      const ds = task.pairs.map(pair => Math.log(endpointValue(pair.arms.on, endpoint.id) / endpointValue(pair.arms.off, endpoint.id)));
      within.push(...ds.map(d => (d - mean(ds)) ** 2));
    }
    const sigma2 = within.length > tasks.length ? within.reduce((a, b) => a + b, 0) / (within.length - tasks.length) : 0;
    const observedMeansVariance = variance(taskMeans);
    endpoints[endpoint.id] = { tau_squared_between_task: round(Math.max(0, observedMeansVariance - sigma2 / metadata.repetitions)), sigma_squared_within_task: round(sigma2), observed_task_mean_variance: round(observedMeansVariance), task_count: tasks.length, repetitions: metadata.repetitions };
  }
  return endpoints;
}

export function chooseAllocation(components) {
  const targetK = Math.max(PROTOCOL.k_floor, PROTOCOL.confirmation_allocation.k_min), multiple = PROTOCOL.confirmation_allocation.k_multiple;
  const z = 1.2815515655446004, alpha = PROTOCOL.alpha;
  let best;
  for (let r = 1; r <= 1000; r++) {
    let k = Math.ceil(targetK / multiple) * multiple;
    while (k <= 10000) {
      let ok = true;
      for (const endpoint of PROTOCOL.endpoints) {
        const v = components[endpoint.id];
        const delta = Math.abs(Math.log(1 - endpoint.prospective_reduction_target));
        const required = (tQuantile(1 - alpha, k - 1) + z) ** 2 * (v.tau_squared_between_task + v.sigma_squared_within_task / r) / (delta ** 2);
        if (k + 1e-12 < required) { ok = false; break; }
      }
      if (ok) break;
      k += multiple;
    }
    if (k > 10000) continue;
    const score = k * r;
    if (!best || score < best.score || (score === best.score && r < best.repetitions)) {
      const achieved = {};
      for (const endpoint of PROTOCOL.endpoints) {
        const v = components[endpoint.id], delta = Math.abs(Math.log(1 - endpoint.prospective_reduction_target));
        const se = Math.sqrt((v.tau_squared_between_task + v.sigma_squared_within_task / r) / k);
        achieved[endpoint.id] = { standard_error: round(se), power: round(normalCDF(delta / se - tQuantile(1 - alpha, k - 1))) };
      }
      best = { tasks: k, repetitions: r, pairs: k * r, achieved_power: achieved, score };
    }
    if (r > 1 && best && r * Math.max(targetK, 4) > best.score) break;
  }
  if (!best) throw new Error('no feasible allocation found within search limits');
  delete best.score;
  return best;
}

async function campaign(campaignId) {
  if (!/^[A-Za-z0-9._-]+$/.test(campaignId) || campaignId.startsWith('.')) throw new Error('invalid --campaign id');
  const dir = path.resolve(ROOT, '.tmp', 'bench', campaignId);
  if (path.dirname(dir) !== path.resolve(ROOT, '.tmp', 'bench')) throw new Error('campaign path escapes .tmp/bench');
  const metadata = JSON.parse(await retryIO(() => readFile(path.join(dir, 'campaign.json'), 'utf8')));
  const text = await retryIO(() => readFile(path.join(dir, 'attempts.jsonl'), 'utf8'));
  const records = [];
  for (const line of text.split(/\r?\n/).filter(Boolean)) {
    try { records.push(JSON.parse(line)); } catch { /* interrupted ledger tails are ignored like the runner */ }
  }
  return { dir, metadata, records };
}
export function validateProvenance(metadata, records, campaignId, expectedAction) {
  if (metadata.campaign_id !== campaignId || metadata.action !== expectedAction) throw new Error('campaign identity or action mismatch');
  const identity = ['engine_sha', 'harness_sha', 'suite_hash'];
  for (const field of identity) if (typeof metadata[field] !== 'string' || !metadata[field]) throw new Error(`missing campaign provenance: ${field}`);
  if (!records.length) throw new Error('attempt ledger is empty');
  for (const field of [...PROVENANCE, 'platform']) {
    const values = new Set(records.map(row => row[field]));
    if (values.size !== 1 || values.has(undefined) || values.has(null) || values.has('')) throw new Error(`missing or mixed attempt provenance: ${field}`);
    if (field in metadata && records[0][field] !== metadata[field]) throw new Error(`attempt provenance mismatch: ${field}`);
  }
  for (const row of records) for (const field of identity) if (row[field] !== metadata[field]) throw new Error(`attempt provenance mismatch: ${field}`);
  if (new Set(records.map(row => row.platform)).size !== 1) throw new Error('mixed operating system cohorts');
  if (records.length !== records[0].n_pairs * 2 || records[0].n_on !== records[0].n_pairs || records[0].n_off !== records[0].n_pairs) throw new Error('attempt counts do not match campaign accounting');
  return records[0].platform;
}
function descriptive(tasks) {
  const data = {};
  const all = tasks.flatMap(task => task.pairs.flatMap(pair => [pair.arms.on, pair.arms.off]));
  const by = (rows, name) => {
    const out = {};
    for (const arm of ['on', 'off']) {
      const selected = rows.filter(row => row.arm === arm);
      out[arm] = { passed: selected.filter(row => row.passed === true).length, attempted: selected.length, pass_rate: selected.length ? round(selected.filter(row => row.passed === true).length / selected.length) : 0 };
    }
    data[name] = out;
  };
  by(all, 'all');
  for (const taskClass of CLASSES) by(all.filter(row => row.task_class === taskClass), taskClass);
  const pairedOutcomes = { both_pass: 0, on_only_pass: 0, off_only_pass: 0, neither_pass: 0 };
  for (const task of tasks) for (const pair of task.pairs) {
    const on = pair.arms.on.passed === true, off = pair.arms.off.passed === true;
    pairedOutcomes[on ? (off ? 'both_pass' : 'on_only_pass') : (off ? 'off_only_pass' : 'neither_pass')]++;
  }
  const failureReasons = { on: {}, off: {} };
  for (const row of all.filter(attempt => attempt.passed !== true)) {
    const reason = row.attempt_status === 'completed' ? (row.passed === false ? 'grading_failed' : 'not_passed') : row.attempt_status;
    failureReasons[row.arm][reason] = (failureReasons[row.arm][reason] ?? 0) + 1;
  }
  return { pass_rates: data, paired_outcomes: pairedOutcomes, failure_reasons: failureReasons };
}
export function buildReport(metadata, records, campaignId) {
  const platform = validateProvenance(metadata, records, campaignId, 'run');
  const pairs = groups(records), tasks = taskRows(pairs);
  if (pairs.length !== metadata.n_pairs || tasks.length !== metadata.n_tasks || records.length !== metadata.n_pairs * 2) throw new Error('attempt ledger does not match declared task and pair counts');
  if (tasks.length < PROTOCOL.k_floor) throw new Error(`confirmation report requires at least ${PROTOCOL.k_floor} tasks`);
  if (tasks.some(task => task.pairs.length !== metadata.repetitions || task.pairs.some((pair, i) => pair.repetition !== i + 1))) throw new Error('task repetitions do not match campaign metadata');
  for (const task of tasks) if (!CLASSES.includes(task.task_class)) throw new Error(`unknown task class: ${task.task_class}`);
  for (const pair of pairs) if (pair.arms.on.repetition !== pair.arms.off.repetition) throw new Error('paired repetition mismatch');
  const claims = {};
  for (const endpoint of PROTOCOL.endpoints) {
    const bound = upperBound(pairedTaskLogMeans(tasks, endpoint.id));
    claims[endpoint.id] = { ...bound, alpha: endpoint.alpha, decision: bound.upper_log_ratio < 0 ? 'supported' : 'not_supported' };
  }
  const provenance = Object.fromEntries(PROVENANCE.map(field => [field, records[0][field]]));
  const descriptions = descriptive(tasks);
  const result = { provenance, platform, method: 'task-level paired log-ratio means; one-sided t upper bound', deadline_penalty: 'unsuccessful runs charged the predeclared verified_wall_time_ms deadline', claims, descriptive_pass_rates: descriptions.pass_rates, paired_outcomes: descriptions.paired_outcomes, failure_reasons: descriptions.failure_reasons };
  const jsonText = JSON.stringify(result, (key, value) => {
    if (typeof value === 'number' && !Number.isFinite(value)) throw new Error(`non-finite report statistic: ${key}`);
    return value;
  }, 2) + '\n';
  const lines = [
    '# Benchmark report', '',
    `Campaign: ${campaignId}`, `Platform cohort: ${platform}`,
    `Provenance: engine_sha=${provenance.engine_sha}; harness_sha=${provenance.harness_sha}; suite_hash=${provenance.suite_hash}; campaign_id=${provenance.campaign_id}; n_tasks=${provenance.n_tasks}; repetitions=${provenance.repetitions}; n_pairs=${provenance.n_pairs}; n_on=${provenance.n_on}; n_off=${provenance.n_off}`,
    '', `Inference: task-level paired log-ratio means, one-sided Student t upper bounds at alpha=${PROTOCOL.alpha}. Failed attempts remain in all-attempt denominators; unsuccessful wall-time observations use the predeclared deadline.`, '',
    '| Endpoint | Tasks | Mean log ratio | Upper log bound | Savings lower bound | Savings claim |', '|---|---:|---:|---:|---:|---|'
  ];
  for (const endpoint of PROTOCOL.endpoints) { const c = claims[endpoint.id]; lines.push(`| ${endpoint.name} | ${c.tasks} | ${fmt(c.mean_log_ratio)} | ${fmt(c.upper_log_ratio)} | ${fmt(c.savings_lower_bound)} | ${c.decision} |`); }
  lines.push('', 'Pass rates (descriptive only):', '', '| Group | Arm | Passed | Attempted | Pass rate |', '|---|---|---:|---:|---:|');
  for (const [group, arms] of Object.entries(result.descriptive_pass_rates)) for (const arm of ['on', 'off']) { const v = arms[arm]; lines.push(`| ${group} | ${arm} | ${v.passed} | ${v.attempted} | ${fmt(v.pass_rate * 100)}% |`); }
  lines.push('', 'Paired outcomes:', '', '| Both passed | On only passed | Off only passed | Neither passed |', '|---:|---:|---:|---:|', `| ${result.paired_outcomes.both_pass} | ${result.paired_outcomes.on_only_pass} | ${result.paired_outcomes.off_only_pass} | ${result.paired_outcomes.neither_pass} |`, '', 'Failure reasons by arm:', '');
  for (const arm of ['on', 'off']) lines.push(`- ${arm}: ${Object.keys(result.failure_reasons[arm]).length ? Object.entries(result.failure_reasons[arm]).map(([reason, count]) => `${reason}=${count}`).join(', ') : 'none'}`);
  lines.push('');
  const markdown = lines.join('\n') + '\n';
  return { jsonText, markdown, result };
}
export async function analyzeReport(campaignId) {
  const { dir, metadata, records } = await campaign(campaignId);
  const result = buildReport(metadata, records, campaignId);
  await Promise.all([writeFile(path.join(dir, 'report.json'), result.jsonText), writeFile(path.join(dir, 'report.md'), result.markdown)]);
  return result;
}

async function main(argv) {
  const [action, ...args] = argv, opts = {};
  for (let i = 0; i < args.length; i++) { if (args[i] !== '--campaign' || opts.campaign) throw new Error('usage: node bench/analyze.mjs allocate|report --campaign <id>'); opts.campaign = args[++i]; }
  if (!['allocate', 'report'].includes(action) || !opts.campaign) throw new Error('usage: node bench/analyze.mjs allocate|report --campaign <id>');
  const { dir, metadata, records } = await campaign(opts.campaign);
  if (action === 'allocate') {
    validateProvenance(metadata, records, opts.campaign, 'calibrate');
    const varianceComponents = calibrationVariance(records, metadata), allocation = chooseAllocation(varianceComponents);
    const provenance = Object.fromEntries(PROVENANCE.map(field => [field, records[0][field]]));
    const result = { provenance, variance_components: varianceComponents, allocation, formula: PROTOCOL.confirmation_allocation.k_formula };
    await writeFile(path.join(dir, 'allocation.json'), JSON.stringify(result, null, 2) + '\n');
    process.stdout.write(JSON.stringify(result, null, 2) + '\n');
  } else {
    const result = await analyzeReport(opts.campaign);
    process.stdout.write(result.markdown);
  }
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main(process.argv.slice(2)).catch(error => { console.error(error.stack || error.message); process.exitCode = 1; });
