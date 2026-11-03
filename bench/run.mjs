import { spawn } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
import { mkdir, readFile, readdir, rm, stat, writeFile, appendFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const PROTOCOL = JSON.parse(await readFile(path.join(ROOT, 'bench/protocol.json'), 'utf8'));
const RETRIES = 6;
const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
const sha = value => createHash('sha256').update(value).digest('hex');
const json = value => JSON.stringify(value, null, 2) + '\n';

export async function retryIO(fn, attempts = RETRIES) {
  for (let i = 0; ; i++) {
    try { return await fn(); }
    catch (error) {
      if (!['EBUSY', 'EPERM', 'EACCES'].includes(error.code) || i + 1 >= attempts) throw error;
      await wait(Math.min(50 * 2 ** i, 1000));
    }
  }
}

export function armSettings(arm, disabledPlugins = []) {
  if (!['on', 'off'].includes(arm)) throw new Error(`invalid arm: ${arm}`);
  return { permissions: { defaultMode: 'bypassPermissions' }, enabledPlugins: { ...Object.fromEntries(disabledPlugins.map(key => [key, false])), [PROTOCOL.pinned_engine.treatment_plugin_key]: arm === 'on' } };
}

export function childEnvironment(env, stateDir) {
  return { ...Object.fromEntries(Object.entries(env).filter(([key]) => key !== 'CLAUDECODE' && !key.startsWith('CLAUDE_CODE_') && key !== 'CLAUDE_EFFORT' && key !== 'CLAUDE_PID')), PURSER_STATE_DIR: stateDir };
}

async function userPurserPlugins() {
  const configDir = path.resolve(process.env.CLAUDE_CONFIG_DIR || path.join(homedir(), '.claude'));
  let settings;
  try { settings = JSON.parse(await retryIO(() => readFile(path.join(configDir, 'settings.json'), 'utf8'))); }
  catch (error) { if (error.code === 'ENOENT') return []; throw error; }
  const inline = PROTOCOL.pinned_engine.treatment_plugin_key;
  return Object.entries(settings.enabledPlugins ?? {}).filter(([key, enabled]) => key.startsWith('purser@') && key !== inline && enabled === true).map(([key]) => key);
}

function authFailure(events) {
  return events.some(event => event.type === 'result' && event.is_error === true && (
    event.terminal_reason === 'api_error' ||
    (typeof event.result === 'string' && event.result.includes('Not logged in'))
  ));
}

function permissionFailure(events) {
  return events.some(event => event.type === 'result' && Array.isArray(event.permission_denials) && event.permission_denials.length > 0);
}

export function usageFromTranscript(input) {
  const events = typeof input === 'string'
    ? input.split(/\r?\n/).filter(Boolean).map(line => JSON.parse(line))
    : input;
  const main = Object.fromEntries(PROTOCOL.endpoints[0].token_categories.map(k => [k, 0]));
  const messages = new Map(), workerMessages = new Set(), workers = new Map();
  let aggregate;
  for (const event of events) {
    const data = event.message ?? event;
    const usage = data.usage ?? data.message?.usage;
    if (!usage || typeof usage !== 'object') continue;
    const side = event.isSidechain === true || event.sidechain === true || data.isSidechain === true ||
      event.parent_tool_use_id != null || data.parent_tool_use_id != null ||
      (event.session_id && event.main_session_id && event.session_id !== event.main_session_id);
    if (event.type === 'result') {
      if (!side) aggregate = usage;
      continue;
    }
    if (!side && event.type !== 'assistant' && data.role !== 'assistant') continue;
    const id = data.id ?? data.message_id ?? (side ? event.request_id ?? event.id : undefined);
    if (!side && !id) throw new Error('main assistant message id is missing');
    if (side) {
      if (id && workerMessages.has(id)) continue;
      if (id) workerMessages.add(id);
      const key = `${usage.provider ?? data.provider ?? 'unknown'}|${usage.model ?? data.model ?? 'unknown'}|${data.role ?? event.role ?? 'worker'}`;
      const current = workers.get(key) ?? { input_tokens: 0, cache_read_input_tokens: 0, cache_creation_input_tokens: 0, output_tokens: 0, complete: true };
      for (const category of Object.keys(main)) {
        if (!Number.isFinite(usage[category])) current.complete = false;
        else current[category] += usage[category];
      }
      workers.set(key, current); continue;
    }
    const messageUsage = messages.get(id) ?? Object.fromEntries(Object.keys(main).map(category => [category, 0]));
    for (const category of Object.keys(main)) {
      if (!Number.isFinite(usage[category])) throw new Error(`incomplete main usage: ${category}`);
      messageUsage[category] = Math.max(messageUsage[category], usage[category]);
    }
    messages.set(id, messageUsage);
  }
  if (!messages.size) throw new Error('main transcript usage is missing');
  for (const usage of messages.values()) for (const category of Object.keys(main)) main[category] += usage[category];
  // The result event is authoritative for total output; assistant events can be partial snapshots.
  if (aggregate && Number.isFinite(aggregate.output_tokens)) main.output_tokens = aggregate.output_tokens;
  return { main, aggregate: aggregate ?? 'unknown', reconciliation_mismatch: aggregate ? !Number.isFinite(aggregate.output_tokens) || Object.keys(main).filter(key => key !== 'output_tokens').some(key => main[key] !== aggregate[key]) : 'unknown', workers: [...workers].map(([key, value]) => {
    const [provider, model, role] = key.split('|');
    return { provider, model, role, usage: value.complete ? value : 'unknown' };
  }) };
}

function command(exe, args, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(exe, args, { shell: false, windowsHide: process.platform === 'win32', stdio: ['pipe', 'pipe', 'pipe'], ...options });
    let stdout = '', stderr = '';
    child.stdout.setEncoding('utf8'); child.stderr.setEncoding('utf8');
    child.stdout.on('data', chunk => { stdout += chunk; }); child.stderr.on('data', chunk => { stderr += chunk; });
    child.once('error', reject);
    child.once('close', (code, signal) => resolve({ code, signal, stdout, stderr, child }));
    if (options.input != null) child.stdin.end(options.input); else child.stdin.end();
  });
}

export async function runProcess(exe, args, { cwd, env, input, deadlineSeconds }) {
  return new Promise((resolve, reject) => {
    const detached = process.platform !== 'win32';
    const child = spawn(exe, args, { cwd, env, shell: false, detached, windowsHide: process.platform === 'win32', stdio: ['pipe', 'pipe', 'pipe'] });
    const chunks = { stdout: [], stderr: [] };
    for (const name of Object.keys(chunks)) child[name].on('data', b => chunks[name].push(b));
    child.once('error', reject);
    let timedOut = false;
    const killTree = async () => {
      if (process.platform === 'win32') await command('taskkill.exe', ['/PID', String(child.pid), '/T', '/F']).catch(() => {});
      else { try { process.kill(-child.pid, 'SIGKILL'); } catch {} }
    };
    const timer = setTimeout(() => { timedOut = true; void killTree(); }, deadlineSeconds * 1000);
    child.once('close', (code, signal) => {
      clearTimeout(timer);
      resolve({ code, signal, timedOut, stdout: Buffer.concat(chunks.stdout).toString('utf8'), stderr: Buffer.concat(chunks.stderr).toString('utf8') });
    });
    child.stdin.end(input);
  });
}

async function git(args, cwd = ROOT) {
  const result = await command('git', args, { cwd });
  if (result.code !== 0) throw new Error(`git ${args.join(' ')} failed: ${result.stderr}`);
  return result.stdout.trim();
}

export async function grade(target, m, campaignDir) {
  const verify = path.join(campaignDir, 'grading', `${m.id}-${randomUUID()}`);
  await mkdir(path.dirname(verify), { recursive: true });
  const clone = await command('git', ['clone', '--quiet', '--no-hardlinks', ROOT, verify]);
  if (clone.code) throw new Error(`grading clone failed: ${clone.stderr}`);
  await git(['checkout', '--detach', m.start_commit], verify);
  const excluded = (m.check.files ?? []).map(file => `:(exclude)${file}`);
  await git(['add', '-A', '-N'], target);
  const diff = await command('git', ['diff', '--binary', m.start_commit, '--', '.', ...excluded], { cwd: target });
  if (diff.code) throw new Error(`production diff failed: ${diff.stderr}`);
  if (diff.stdout) {
    const applied = await command('git', ['apply', '--whitespace=nowarn', '-'], { cwd: verify, input: diff.stdout });
    if (applied.code) throw new Error(`production diff rejected: ${applied.stderr}`);
  }
  for (const file of m.check.files ?? []) {
    const shown = await command('git', ['show', `${m.source_commit}:${file}`], { cwd: ROOT });
    if (shown.code) throw new Error(`cannot retrieve trusted check ${file}: ${shown.stderr}`);
    const dest = path.join(verify, file);
    await mkdir(path.dirname(dest), { recursive: true });
    await writeFile(dest, shown.stdout);
  }
  let structural = true;
  for (const assertion of m.check.structural ?? []) {
    const content = await readFile(path.join(verify, assertion.path), 'utf8').catch(() => '');
    if (assertion.must_contain != null && !content.includes(assertion.must_contain)) structural = false;
    if (assertion.must_not_contain != null && content.includes(assertion.must_not_contain)) structural = false;
  }
  let check = { code: 1, stdout: '', stderr: 'structural assertion failed' };
  if (structural) check = await runProcess(m.check.command[0], m.check.command.slice(1), { cwd: verify, input: '', deadlineSeconds: m.deadline_seconds });
  await retryIO(() => rm(verify, { recursive: true, force: true }));
  return { passed: structural && !check.timedOut && check.code === 0, ...(check.timedOut ? { attempt_status: 'grading_timeout' } : {}), check_exit_code: check.code, check_stdout: check.stdout, check_stderr: check.stderr };
}

export async function ensureEngine(dir, dryRun) {
  const commit = PROTOCOL.pinned_commit;
  if (dryRun) return { commit, dir, sha: commit };
  if (!(await stat(dir).catch(() => null))) {
    await mkdir(path.dirname(dir), { recursive: true });
    const clone = await command('git', ['clone', '--no-checkout', ROOT, dir]);
    if (clone.code !== 0) throw new Error(`engine clone failed: ${clone.stderr}`);
  }
  const actual = await git(['rev-parse', 'HEAD'], dir);
  if (actual !== commit) {
    await git(['fetch', '--depth=1', 'origin', commit], dir);
  }
  await git(['checkout', '--detach', commit], dir);
  const verified = await git(['rev-parse', 'HEAD'], dir);
  if (verified !== commit) throw new Error(`pinned engine mismatch: ${verified}`);
  if (!(await stat(path.join(dir, '.claude-plugin', 'plugin.json'))).isFile()) throw new Error('pinned engine plugin.json is missing');
  return { commit: verified, dir, sha: verified };
}

async function resolveClaude() {
  const request = process.env.BENCH_CLAUDE || PROTOCOL.cli.command;
  let picked = request;
  if (!path.isAbsolute(picked) && picked.includes(path.sep)) picked = path.resolve(picked);
  if (!path.isAbsolute(picked) && !picked.includes(path.sep)) {
    const extensions = process.platform === 'win32' ? (process.env.PATHEXT || '.EXE;.CMD;.BAT').split(';') : [''];
    for (const dir of (process.env.PATH || '').split(path.delimiter)) {
      for (const ext of extensions) {
        const candidate = path.join(dir, picked + ext);
        if (await stat(candidate).catch(() => null)) { picked = candidate; break; }
      }
      if (path.isAbsolute(picked)) break;
    }
  }
  if (process.platform === 'win32' && /\.cmd$/i.test(picked)) {
    const contents = await readFile(picked, 'utf8');
    const js = contents.match(/"%~dp0\\([^" ]+\.js)"/i)?.[1];
    if (!js) throw new Error(`cannot resolve Windows CLI shim: ${picked}`);
    const entry = path.resolve(path.dirname(picked), js);
    return { exe: process.execPath, prefix: [entry], hashPath: entry };
  }
  if (/\.m?js$/i.test(picked)) { const entry = path.resolve(picked); return { exe: process.execPath, prefix: [entry], hashPath: entry }; }
  return { exe: picked, prefix: [], hashPath: picked };
}

async function taskDirs(pattern) {
  const base = path.join(ROOT, 'bench', 'tasks');
  const found = [];
  async function visit(dir) {
    for (const entry of await readdir(dir, { withFileTypes: true }).catch(() => [])) {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) await visit(full);
      else if (entry.name === 'task.json') {
        const rel = path.relative(base, path.dirname(full)).split(path.sep).join('/');
        if (!pattern || globMatch(rel, pattern)) found.push(path.dirname(full));
      }
    }
  }
  await visit(base);
  return found.sort();
}
function globMatch(value, pattern) {
  const escaped = pattern.split('*').map(s => s.replace(/[.+?^${}()|[\]\\]/g, '\\$&')).join('.*');
  return new RegExp(`^${escaped}$`).test(value);
}
function parseArgs(argv) {
  const [action, ...rest] = argv;
  if (!['preflight', 'calibrate', 'run'].includes(action)) throw new Error('usage: node bench/run.mjs preflight|calibrate|run --campaign <id> [--tasks <glob>] [--reps N] [--dry-run] [--resume]');
  const opts = { action, reps: action === 'calibrate' ? 2 : 1, dryRun: false };
  for (let i = 0; i < rest.length; i++) {
    const a = rest[i];
    if (a === '--dry-run') opts.dryRun = true;
    else if (a === '--resume') opts.resume = true;
    else if (['--campaign', '--tasks', '--reps'].includes(a)) opts[a.slice(2)] = rest[++i];
    else throw new Error(`unknown argument: ${a}`);
  }
  if (!opts.campaign || opts.campaign.startsWith('.') || !/^[A-Za-z0-9._-]+$/.test(opts.campaign)) throw new Error('--campaign is required and must be a safe id');
  opts.reps = Number(opts.reps);
  if (!Number.isInteger(opts.reps) || opts.reps < 1) throw new Error('--reps must be a positive integer');
  return opts;
}

async function preflight(campaignDir, dryRun) {
  const claude = await resolveClaude();
  const version = await command(claude.exe, [...claude.prefix, '--version']);
  if (version.code !== 0 || !version.stdout.trim().includes(PROTOCOL.cli.version)) throw new Error(`Claude CLI must be ${PROTOCOL.cli.version}; got ${version.stdout.trim() || version.stderr.trim()}`);
  const engine = await ensureEngine(path.join(campaignDir, 'engine'), dryRun);
  return { claude, version: version.stdout.trim(), engine };
}

function shuffleOrders(n, seed) {
  let x = seed >>> 0;
  const rand = () => { x ^= x << 13; x ^= x >>> 17; x ^= x << 5; return x >>> 0; };
  const orders = Array.from({ length: n }, (_, i) => i % 2 ? ['off', 'on'] : ['on', 'off']);
  for (let i = orders.length - 1; i > 0; i--) { const j = rand() % (i + 1); [orders[i], orders[j]] = [orders[j], orders[i]]; }
  return orders;
}

export async function campaignMetadata(campaignDir, expected, resume) {
  const file = path.join(campaignDir, 'campaign.json');
  const conflict = () => new Error(`Campaign "${expected.campaign_id}" already exists; use a new id, or --resume with matching campaign metadata.`);
  let stored;
  try { stored = JSON.parse(await retryIO(() => readFile(file, 'utf8'))); }
  catch (error) { if (error instanceof SyntaxError) throw conflict(); if (error.code !== 'ENOENT') throw error; }
  if (stored !== undefined) {
    if (!resume || !stored || Object.keys(expected).some(key => stored[key] !== expected[key]) ||
        !Number.isInteger(stored.seed) || !Array.isArray(stored.schedule) || stored.schedule.length !== expected.n_pairs ||
        stored.schedule.some(order => !Array.isArray(order) || order.length !== 2 || !order.includes('on') || !order.includes('off'))) throw conflict();
    return stored;
  }
  const seed = Number.parseInt(sha(`${expected.campaign_id}:${Date.now()}`).slice(0, 8), 16) >>> 0;
  const metadata = { ...expected, seed, schedule: shuffleOrders(expected.n_pairs, seed), created_at: new Date().toISOString() };
  try { await writeFile(file, json(metadata), { flag: 'wx' }); }
  catch (error) { if (error.code === 'EEXIST') throw conflict(); throw error; }
  return metadata;
}

export async function terminalAttempts(ledger) {
  const terminal = new Set();
  let text;
  try { text = await retryIO(() => readFile(ledger, 'utf8')); }
  catch (error) { if (error.code === 'ENOENT') return terminal; throw error; }
  for (const line of text.split(/\r?\n/).filter(Boolean)) {
    let record;
    try { record = JSON.parse(line); }
    // Interrupted appends may precede records written by a later resume.
    catch { continue; }
    if (['completed', 'failed', 'deadline', 'grading_timeout', 'dry_run'].includes(record.attempt_status) && !['auth', 'permission'].includes(record.failure_reason)) {
      terminal.add(JSON.stringify([record.task_id, record.repetition, record.arm]));
    }
  }
  return terminal;
}

async function main() {
  const opts = parseArgs(process.argv.slice(2));
  const campaignBase = path.resolve(ROOT, '.tmp', 'bench');
  const campaignDir = path.resolve(campaignBase, opts.campaign);
  if (path.dirname(campaignDir) !== campaignBase) throw new Error('campaign directory must stay inside .tmp/bench');
  await mkdir(campaignDir, { recursive: true });
  const pf = await preflight(campaignDir, opts.dryRun);
  const disabledPlugins = await userPurserPlugins();
  const dirs = await taskDirs(opts.tasks);
  if (opts.action === 'preflight') { console.log(json({ ok: true, cli: pf.version, engine_sha: pf.engine.sha })); return; }
  const split = opts.action === 'calibrate' ? 'calibration' : 'confirmation';
  const tasks = (await Promise.all(dirs.map(async dir => ({ dir, manifest: JSON.parse(await readFile(path.join(dir, 'task.json'), 'utf8')) }))))
    .filter(task => task.manifest.split === split);
  if (!tasks.length) throw new Error(`no ${split} task manifests matched`);
  if (opts.action === 'calibrate' && tasks.length !== PROTOCOL.calibration_size.tasks) {
    throw new Error(`calibration selection must contain exactly ${PROTOCOL.calibration_size.tasks} tasks; got ${tasks.length}`);
  }
  const pairs = [];
  for (const task of tasks) for (let rep = 0; rep < opts.reps; rep++) pairs.push({ task, rep });
  const ledger = path.join(campaignDir, 'attempts.jsonl');
  const suiteParts = await Promise.all(tasks.map(async t => {
    const checkFiles = await Promise.all((t.manifest.check.files ?? []).map(async file => {
      const result = await command('git', ['show', `${t.manifest.source_commit}:${file}`], { cwd: ROOT });
      if (result.code) throw new Error(`cannot hash trusted check ${file}: ${result.stderr}`);
      return { path: file, sha: sha(result.stdout) };
    }));
    return JSON.stringify({ manifest: t.manifest, prompt_sha: sha(await readFile(path.join(t.dir, t.manifest.prompt_file))), check_files: checkFiles });
  }));
  const suiteHash = sha(suiteParts.sort().join('\n'));
  const repoHash = await git(['rev-parse', 'HEAD']);
  const metadata = await campaignMetadata(campaignDir, {
    campaign_id: opts.campaign, action: opts.action, platform: process.platform, engine_sha: pf.engine.sha,
    harness_sha: sha(await readFile(fileURLToPath(import.meta.url))), suite_hash: suiteHash,
    model: process.env.BENCH_MODEL || 'sonnet', effort: process.env.BENCH_EFFORT || 'high',
    dry_run: opts.dryRun, repetitions: opts.reps, n_pairs: pairs.length
  }, opts.resume);
  const { seed, schedule: orders } = metadata;
  const terminal = opts.resume ? await terminalAttempts(ledger) : new Set();
  if (opts.action === 'calibrate' || opts.action === 'run') {
    for (let pi = 0; pi < pairs.length; pi++) {
      const { task, rep } = pairs[pi], order = orders[pi];
      for (const arm of order) {
        if (terminal.has(JSON.stringify([task.manifest.id, rep + 1, arm]))) continue;
        const m = task.manifest, runId = `${arm}-${m.id}-${rep + 1}-${randomUUID()}`;
        const runDir = path.join(campaignDir, 'targets', runId), configDir = path.join(campaignDir, 'config', runId), stateDir = path.join(campaignDir, 'state', runId), transcriptDir = path.join(campaignDir, 'transcripts', runId);
        await Promise.all([runDir, configDir, stateDir, transcriptDir].map(p => mkdir(p, { recursive: true })));
        const configFile = path.join(configDir, 'settings.json'); await writeFile(configFile, json(armSettings(arm, disabledPlugins)));
        const target = path.join(runDir, 'repo');
        if (!opts.dryRun) {
          await command('git', ['clone', '--quiet', '--no-hardlinks', ROOT, target]).then(r => { if (r.code) throw Error(r.stderr); });
          await git(['checkout', '--detach', m.start_commit], target);
          await git(['clean', '-ffdqx'], target);
        }
        const prompt = await readFile(path.join(task.dir, m.prompt_file));
        const session = randomUUID();
        const args = [...pf.claude.prefix, '-p', '--model', metadata.model, '--effort', metadata.effort, '--session-id', session, '--setting-sources', 'project,local', '--settings', configFile, '--plugin-dir', pf.engine.dir, '--output-format', 'stream-json', '--verbose', '--include-hook-events'];
        const started = Date.now();
        let run = { code: 0, timedOut: false, stdout: '', stderr: '' }, outcome = { passed: null };
        if (!opts.dryRun) run = await runProcess(pf.claude.exe, args, { cwd: target, input: prompt, deadlineSeconds: m.deadline_seconds, env: childEnvironment(process.env, stateDir) });
        await writeFile(path.join(transcriptDir, 'stdout.jsonl'), run.stdout);
        await writeFile(path.join(transcriptDir, 'stderr.log'), run.stderr);
        const transcript = await retryIO(() => readFile(path.join(transcriptDir, 'stdout.jsonl'), 'utf8'));
        let parsed = [], transcriptError;
        for (const line of transcript.split(/\r?\n/).filter(Boolean)) {
          try { parsed.push(JSON.parse(line)); }
          catch { transcriptError = 'malformed stream-json transcript'; break; }
        }
        const isAuthFailure = authFailure(parsed);
        const isPermissionFailure = permissionFailure(parsed);
        let usage = { main: 'unknown', workers: 'unknown' }, accountingError;
        try {
          if (transcriptError) throw new Error(transcriptError);
          const result = usageFromTranscript(parsed); usage = { main: result.main, aggregate: result.aggregate, reconciliation_mismatch: result.reconciliation_mismatch, workers: result.workers.length ? result.workers : 'unknown' };
          if (result.reconciliation_mismatch === true) accountingError = 'main transcript usage does not match aggregate result usage';
        } catch (e) { accountingError = e.message; }
        if (!opts.dryRun && !isAuthFailure && !isPermissionFailure && !run.timedOut && run.code === 0) outcome = await grade(target, m, campaignDir);
        // The contract measures launch through independent grading.
        const elapsed = Date.now() - started;
        const isEnvironmentFailure = isAuthFailure || isPermissionFailure;
        const result = { ...metadata, n_tasks: tasks.length, repetitions: opts.reps, n_pairs: pairs.length, n_on: pairs.length, n_off: pairs.length, task_id: m.id, task_class: m.class, repetition: rep + 1, arm, order, session_id: session, attempt_status: isEnvironmentFailure ? 'failed' : opts.dryRun ? 'dry_run' : run.timedOut ? 'deadline' : run.code === 0 ? 'completed' : 'failed', ...(isAuthFailure ? { failure_reason: 'auth' } : isPermissionFailure ? { failure_reason: 'permission' } : {}), exit_code: run.code, timed_out: run.timedOut, elapsed_ms: elapsed, verified_wall_time_ms: outcome.passed === true ? elapsed : m.deadline_seconds * 1000, usage, accounting_error: accountingError, prompt_sha: sha(prompt), settings_sha: sha(await readFile(configFile)), transcript_sha: sha(transcript), binary_sha: sha(await readFile(pf.claude.hashPath)), source_commit: m.source_commit, start_commit: m.start_commit, repository_head: repoHash, ...outcome, worker_usage: usage.workers };
        await writeFile(path.join(campaignDir, `${runId}.json`), json(result));
        // Environment failures stay inspectable but must not enter the terminal analysis ledger.
        if (!isEnvironmentFailure) await appendFile(ledger, '\n' + JSON.stringify(result) + '\n');
        if (!opts.dryRun) await retryIO(() => rm(runDir, { recursive: true, force: true }));
        if (isAuthFailure) throw new Error('Claude authentication failed (Not logged in). Run /login in the default Claude config, then resume the campaign.');
        if (isPermissionFailure) throw new Error('Claude permission denied one or more tool requests. Check the per-run permission settings before resuming the campaign.');
      }
    }
  }
  console.log(json({ ok: true, campaign_id: opts.campaign, tasks: tasks.length, pairs: pairs.length, seed }));
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch(error => { console.error(error.stack || error.message); process.exitCode = 1; });
}
