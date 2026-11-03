import test, { after } from 'node:test';
import assert from 'node:assert/strict';
import { appendFile, chmod, mkdir, mkdtemp, readdir, rm, rmdir, readFile, writeFile } from 'node:fs/promises';
import { execFileSync, spawn } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { armSettings, campaignMetadata, childEnvironment, cleanupRunDir, ensureEngine, grade, retryIO, runProcess, terminalAttempts, usageFromTranscript } from '../run.mjs';

const scratchRoot = fileURLToPath(new URL('../../.tmp/bench-test/', import.meta.url));
const protocol = JSON.parse(await readFile(fileURLToPath(new URL('../protocol.json', import.meta.url)), 'utf8'));
const hostBinary = `bin/purser-hook-${process.platform === 'win32' ? 'windows' : process.platform === 'darwin' ? 'darwin' : process.platform}-${({ x64: 'amd64', ia32: '386', arm: 'arm', arm64: 'arm64' })[process.arch] ?? process.arch}${process.platform === 'win32' ? '.exe' : ''}`;
await mkdir(scratchRoot, { recursive: true });
after(() => retryIO(() => rmdir(scratchRoot)).catch(error => {
  if (error.code !== 'ENOENT' && error.code !== 'ENOTEMPTY') throw error;
}));

test('resume skips terminal arms, keeps partial pairs pending, and ignores interrupted ledger tails', async t => {
  const scratch = await mkdtemp(path.join(scratchRoot, 'bench-ledger-'));
  t.after(() => retryIO(() => rm(scratch, { recursive: true, force: true })));
  const ledger = path.join(scratch, 'attempts.jsonl');
  const records = [
    { task_id: 'done', repetition: 1, arm: 'on', attempt_status: 'completed' },
    { task_id: 'done', repetition: 1, arm: 'off', attempt_status: 'failed' },
    { task_id: 'partial', repetition: 2, arm: 'on', attempt_status: 'grading_timeout' },
    { task_id: 'partial', repetition: 2, arm: 'off', attempt_status: 'running' }
  ];
  const prefix = records.map(record => JSON.stringify(record)).join('\n') + '\n';
  await writeFile(ledger, prefix + '{"task_id":"partial","repetition":2,"arm":"off"');
  const terminal = await terminalAttempts(ledger);
  assert.equal(terminal.size, 3);
  assert.ok(['on', 'off'].every(arm => terminal.has(JSON.stringify(['done', 1, arm]))));
  assert.deepEqual(['on', 'off'].filter(arm => !terminal.has(JSON.stringify(['partial', 2, arm]))), ['off']);
  await appendFile(ledger, '\n' + JSON.stringify({ task_id: 'partial', repetition: 2, arm: 'off', attempt_status: 'deadline' }) + '\n');
  const resumed = await terminalAttempts(ledger);
  assert.ok(['on', 'off'].every(arm => resumed.has(JSON.stringify(['partial', 2, arm]))));
  assert.ok((await readFile(ledger, 'utf8')).startsWith(prefix));
});

test('grading deadline kills check descendants and returns a grading timeout instead of a pass', async t => {
  const repo = fileURLToPath(new URL('../../', import.meta.url));
  const scratch = await mkdtemp(path.join(scratchRoot, 'bench-grade-timeout-'));
  t.after(() => retryIO(() => rm(scratch, { recursive: true, force: true })));
  const target = path.join(scratch, 'target');
  const options = { cwd: repo, shell: false, windowsHide: true, encoding: 'utf8' };
  const commit = execFileSync('git', ['rev-parse', 'HEAD'], options).trim();
  execFileSync('git', ['clone', '--quiet', '--no-hardlinks', repo, target], options);
  const marker = path.join(scratch, 'orphan.txt');
  const childCode = `setTimeout(()=>require('fs').writeFileSync(${JSON.stringify(marker)},'alive'),1200)`;
  const checkCode = `require('child_process').spawn(process.execPath,['-e',${JSON.stringify(childCode)}],{stdio:'ignore'}); setTimeout(()=>{},10000)`;
  const result = await grade(target, {
    id: 'grading-timeout', start_commit: commit, source_commit: commit, deadline_seconds: 0.3,
    check: { files: [], command: [process.execPath, '-e', checkCode] }
  }, scratch);
  assert.equal(result.passed, false);
  assert.equal(result.attempt_status, 'grading_timeout');
  await new Promise(resolve => setTimeout(resolve, 1400));
  await assert.rejects(readFile(marker));
});

test('campaign reuse requires matching metadata and explicit resume preserves seed and schedule', async t => {
  const scratch = await mkdtemp(path.join(scratchRoot, 'bench-resume-'));
  t.after(() => retryIO(() => rm(scratch, { recursive: true, force: true })));
  const expected = {
    campaign_id: 'resume-test', action: 'run', suite_hash: 'suite', engine_sha: 'engine', harness_sha: 'harness',
    platform: process.platform, model: 'model', effort: 'high', dry_run: true, repetitions: 2, n_pairs: 4
  };
  const original = await campaignMetadata(scratch, expected, false);
  const bytes = await readFile(path.join(scratch, 'campaign.json'), 'utf8');
  const conflict = /Campaign "resume-test" already exists; use a new id/;
  await assert.rejects(campaignMetadata(scratch, expected, false), conflict);
  for (const key of ['action', 'suite_hash', 'engine_sha', 'harness_sha', 'platform', 'model', 'effort', 'dry_run', 'repetitions', 'n_pairs']) {
    await assert.rejects(campaignMetadata(scratch, { ...expected, [key]: 'changed' }, true), conflict);
  }
  const resumed = await campaignMetadata(scratch, expected, true);
  assert.equal(resumed.seed, original.seed);
  assert.deepEqual(resumed.schedule, original.schedule);
  assert.equal(await readFile(path.join(scratch, 'campaign.json'), 'utf8'), bytes);
});

test('engine archive sha mismatch refuses extraction', async t => {
  const scratch = await mkdtemp(path.join(scratchRoot, 'bench-engine-'));
  t.after(() => retryIO(() => rm(scratch, { recursive: true, force: true })));
  let requested;
  await assert.rejects(ensureEngine(path.join(scratch, 'engine'), false, { download: async url => { requested = url; return Buffer.from('not the pinned ZIP'); } }), /archive sha256 mismatch/);
  assert.equal(requested, protocol.pinned_engine.archive_url);
  await assert.rejects(readFile(path.join(scratch, 'engine', '.claude-plugin', 'plugin.json')), { code: 'ENOENT' });
});

test('engine with missing host binary refuses preflight validation', async t => {
  const scratch = await mkdtemp(path.join(scratchRoot, 'bench-engine-missing-'));
  t.after(() => retryIO(() => rm(scratch, { recursive: true, force: true })));
  const engine = path.join(scratch, 'engine');
  await mkdir(path.join(engine, '.claude-plugin'), { recursive: true });
  await writeFile(path.join(engine, '.claude-plugin', 'plugin.json'), JSON.stringify({ version: protocol.pinned_engine.tag.slice(1) }));
  await writeFile(path.join(engine, '.bench-engine-sha256'), protocol.pinned_engine.archive_sha256);
  await assert.rejects(ensureEngine(engine, false), /host binary is missing/);
});

async function installFixtureEngine(campaignDir, { binary = true } = {}) {
  const engine = path.join(campaignDir, 'engine');
  await mkdir(path.join(engine, '.claude-plugin'), { recursive: true });
  await writeFile(path.join(engine, '.claude-plugin', 'plugin.json'), JSON.stringify({ version: protocol.pinned_engine.tag.slice(1) }));
  if (binary) {
    const executable = path.join(engine, hostBinary);
    await mkdir(path.dirname(executable), { recursive: true });
    await writeFile(executable, 'fixture executable');
    if (process.platform !== 'win32') await chmod(executable, 0o755);
  }
  await writeFile(path.join(engine, '.bench-engine-sha256'), protocol.pinned_engine.archive_sha256);
}

test('grading includes untracked production files and restores trusted checks', async t => {
  const repo = fileURLToPath(new URL('../../', import.meta.url));
  const scratch = await mkdtemp(path.join(scratchRoot, 'bench-grade-'));
  t.after(() => retryIO(() => rm(scratch, { recursive: true, force: true })));
  const target = path.join(scratch, 'target');
  const options = { cwd: repo, shell: false, windowsHide: true, encoding: 'utf8' };
  const commit = execFileSync('git', ['rev-parse', 'HEAD'], options).trim();
  execFileSync('git', ['clone', '--quiet', '--no-hardlinks', repo, target], options);
  const production = 'bench-untracked-production.txt', check = '.claude-plugin/plugin.json';
  const trusted = execFileSync('git', ['show', `${commit}:${check}`], options);
  await writeFile(path.join(target, production), 'new production behavior');
  await writeFile(path.join(target, check), 'agent-edited check');
  const result = await grade(target, {
    id: 'untracked', start_commit: commit, source_commit: commit, deadline_seconds: 5,
    check: { files: [check], command: [process.execPath, '-e', `const fs=require('fs'),assert=require('assert/strict'); assert.equal(fs.readFileSync(${JSON.stringify(production)},'utf8'),'new production behavior'); assert.equal(fs.readFileSync(${JSON.stringify(check)},'utf8'),${JSON.stringify(trusted)});`] }
  }, scratch);
  assert.equal(result.passed, true, result.check_stderr);
});

test('campaign ids cannot resolve to or escape the benchmark base directory', async () => {
  const runner = fileURLToPath(new URL('../run.mjs', import.meta.url));
  for (const campaign of ['.', '..', '.hidden', '../outside']) {
    const result = await runProcess(process.execPath, [runner, 'preflight', '--campaign', campaign, '--dry-run'], {
      env: { ...process.env, BENCH_CLAUDE: 'must-not-be-launched' }, input: '', deadlineSeconds: 5
    });
    assert.equal(result.code, 1, campaign);
    assert.match(result.stderr, /--campaign is required and must be a safe id/, campaign);
  }
});

test('run cleanup retries transient locks and reports persistent locks without throwing', async () => {
  let attempts = 0;
  const removed = await cleanupRunDir('fixture', { durationMs: 100, remove: async () => {
    if (++attempts < 3) throw Object.assign(new Error('locked'), { code: 'EBUSY' });
  } });
  assert.deepEqual(removed, { pending: false });
  assert.equal(attempts, 3);
  const warning = console.warn;
  let warned = '';
  console.warn = message => { warned = message; };
  try {
    const pending = await cleanupRunDir('fixture', { durationMs: 0, remove: async () => { throw Object.assign(new Error('locked'), { code: 'EPERM' }); } });
    assert.equal(pending.pending, true);
    assert.match(warned, /cleanup pending/);
  } finally { console.warn = warning; }
});

test('validate with no matching tasks exits nonzero without starting external tools', async () => {
  const runner = fileURLToPath(new URL('../run.mjs', import.meta.url));
  const result = await runProcess(process.execPath, [runner, 'validate', '--tasks', 'no-such-task-*'], { input: '', deadlineSeconds: 10 });
  assert.equal(result.code, 1);
  assert.match(result.stderr, /no task manifests matched/);
});

test('arm settings differ only at the Purser plugin switch', () => {
  const disabled = ['purser@purser-local', 'purser@other'];
  const on = armSettings('on', disabled), off = armSettings('off', disabled);
  assert.deepEqual(Object.keys(on), Object.keys(off));
  assert.deepEqual(on.permissions, { defaultMode: 'bypassPermissions' });
  assert.deepEqual(off.permissions, { defaultMode: 'bypassPermissions' });
  const key = 'purser@inline';
  assert.deepEqual(Object.keys(on.enabledPlugins), [...disabled, key]);
  for (const disabledKey of disabled) {
    assert.equal(on.enabledPlugins[disabledKey], false);
    assert.equal(off.enabledPlugins[disabledKey], false);
  }
  assert.equal(on.enabledPlugins[key], true);
  assert.equal(off.enabledPlugins[key], false);
});

test('child environment strips Claude parent-session variables and keeps provider variables', () => {
  const env = childEnvironment({
    CLAUDECODE: 'parent', CLAUDE_CODE_SESSION_ID: 'session', CLAUDE_CODE_ENTRYPOINT: 'cli',
    CLAUDE_EFFORT: 'high', CLAUDE_PID: '123', ANTHROPIC_API_KEY: 'untouched', KEEP: 'yes'
  }, 'state-dir');
  assert.deepEqual(env, { ANTHROPIC_API_KEY: 'untouched', KEEP: 'yes', PURSER_STATE_DIR: 'state-dir' });
  assert.equal('CLAUDE_CONFIG_DIR' in childEnvironment({ KEEP: 'yes' }, 'state'), false);
});

test('duplicate message ids contribute once and sidechain usage is separate', () => {
  const main = { role: 'assistant', id: 'msg-1', usage: { input_tokens: 3, cache_read_input_tokens: 2, cache_creation_input_tokens: 1, output_tokens: 4 } };
  const result = usageFromTranscript([
    { message: main }, { message: main },
    { message: { id: 'child-1', parent_tool_use_id: 'tool-1', usage: { provider: 'anthropic', model: 'worker', input_tokens: 30, cache_read_input_tokens: 0, cache_creation_input_tokens: 0, output_tokens: 5 } } }
  ]);
  assert.deepEqual(result.main, { input_tokens: 3, cache_read_input_tokens: 2, cache_creation_input_tokens: 1, output_tokens: 4 });
  assert.equal(result.workers[0].usage.output_tokens, 5);
});

test('result usage reconciles assistant message totals without being added to them', () => {
  const expected = { input_tokens: 3, cache_read_input_tokens: 2, cache_creation_input_tokens: 1, output_tokens: 4 };
  const assistant = { type: 'assistant', message: { id: 'msg-1', role: 'assistant', usage: expected } };
  const events = [assistant, assistant, { type: 'system', usage: expected }, { type: 'result', usage: { ...expected } }];
  const result = usageFromTranscript(events);
  assert.deepEqual(result.main, expected);
  assert.deepEqual(result.aggregate, expected);
  assert.equal(result.reconciliation_mismatch, false);
  events.at(-1).usage.output_tokens++;
  const mismatch = usageFromTranscript(events);
  assert.deepEqual(mismatch.main, { ...expected, output_tokens: 5 });
  assert.equal(mismatch.aggregate.output_tokens, 5);
  assert.equal(mismatch.reconciliation_mismatch, false);
  events.at(-1).usage.cache_read_input_tokens++;
  assert.equal(usageFromTranscript(events).reconciliation_mismatch, true);
});

test('repeated main message ids use growing usage snapshots once and reconcile to result totals', () => {
  const first = { input_tokens: 3, cache_read_input_tokens: 2, cache_creation_input_tokens: 1, output_tokens: 87 };
  const final = { ...first, output_tokens: 3426 };
  const result = usageFromTranscript([
    { type: 'assistant', message: { id: 'msg-1', role: 'assistant', usage: first } },
    { type: 'assistant', message: { id: 'msg-1', role: 'assistant', usage: final } },
    { type: 'result', usage: final }
  ]);
  assert.deepEqual(result.main, final);
  assert.deepEqual(result.aggregate, final);
  assert.equal(result.reconciliation_mismatch, false);
});

test('missing worker token categories are unknown, not zero', () => {
  const result = usageFromTranscript([
    { message: { role: 'assistant', id: 'main', usage: { input_tokens: 0, cache_read_input_tokens: 0, cache_creation_input_tokens: 0, output_tokens: 0 } } },
    { message: { id: 'worker', parent_tool_use_id: 'tool', usage: { input_tokens: 1 } } }
  ]);
  assert.equal(result.workers[0].usage, 'unknown');
});

test('locked transcript reads retry and then succeed', async () => {
  let attempts = 0;
  const value = await retryIO(async () => {
    if (attempts++ < 2) { const error = new Error('sharing violation'); error.code = 'EBUSY'; throw error; }
    return await readFile(fileURLToPath(import.meta.url), 'utf8');
  });
  assert.match(value, /locked transcript reads retry/);
  assert.equal(attempts, 3);
});

test('preflight uses the injected fake Claude executable without network access', async t => {
  const dir = await mkdtemp(path.join(scratchRoot, 'bench-cli-'));
  const campaign = `test-${process.pid}-${Date.now()}`;
  const runner = fileURLToPath(new URL('../run.mjs', import.meta.url));
  const repo = path.resolve(path.dirname(runner), '..');
  const artifacts = path.join(repo, '.tmp', 'bench', campaign);
  t.after(async () => { await retryIO(() => rm(dir, { recursive: true, force: true })); await retryIO(() => rm(artifacts, { recursive: true, force: true })); });
  await installFixtureEngine(artifacts);
  const fake = path.join(dir, 'fake-claude.mjs');
  await writeFile(fake, `process.stdout.write('Claude Code 2.1.317\\n');`);
  const result = await new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [runner, 'preflight', '--campaign', campaign, '--dry-run'], { env: { ...process.env, BENCH_CLAUDE: fake }, shell: false, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '', stderr = '';
    child.stdout.setEncoding('utf8').on('data', chunk => { stdout += chunk; });
    child.stderr.setEncoding('utf8').on('data', chunk => { stderr += chunk; });
    child.once('error', reject); child.once('close', code => resolve({ code, stdout, stderr }));
  });
  assert.equal(result.code, 0, result.stderr);
  assert.match(result.stdout, /2\.1\.317/);
});

test('run selects confirmation tasks and calibrate rejects a narrowed task selection', async t => {
  const dir = await mkdtemp(path.join(scratchRoot, 'bench-split-'));
  const campaign = `test-${process.pid}-${Date.now()}`;
  const runner = fileURLToPath(new URL('../run.mjs', import.meta.url));
  const repo = path.resolve(path.dirname(runner), '..');
  const artifacts = path.join(repo, '.tmp', 'bench', campaign);
  const narrowedArtifacts = path.join(repo, '.tmp', 'bench', `${campaign}-narrow`);
  t.after(async () => { await retryIO(() => rm(dir, { recursive: true, force: true })); await retryIO(() => rm(artifacts, { recursive: true, force: true })); await retryIO(() => rm(narrowedArtifacts, { recursive: true, force: true })); });
  const fake = path.join(dir, 'fake-claude.mjs');
  await writeFile(fake, `process.stdout.write('Claude Code 2.1.317\\n');`);
  const run = await runProcess(process.execPath, [runner, 'run', '--campaign', campaign, '--dry-run'], {
    cwd: repo, env: { ...process.env, BENCH_CLAUDE: fake }, input: '', deadlineSeconds: 30
  });
  assert.equal(run.code, 0, run.stderr);
  assert.match(run.stdout, /"tasks": 12,\s+"pairs": 12/);
  const records = (await readFile(path.join(artifacts, 'attempts.jsonl'), 'utf8')).split(/\r?\n/).filter(Boolean).map(line => JSON.parse(line));
  assert.equal(new Set(records.map(row => row.task_id)).size, 12);
  const taskSplits = new Map();
  for (const file of execFileSync(process.execPath, ['-e', `const fs=require('fs'),path=require('path'); const root=${JSON.stringify(path.join(repo, 'bench', 'tasks'))}; function walk(d){for(const e of fs.readdirSync(d,{withFileTypes:true})){const p=path.join(d,e.name);if(e.isDirectory())walk(p);else if(e.name==='task.json'){const m=JSON.parse(fs.readFileSync(p));console.log(m.id+'\\t'+m.split)}}}walk(root);`], { cwd: repo, encoding: 'utf8' }).trim().split(/\r?\n/)) {
    const [id, split] = file.split('\t'); taskSplits.set(id, split);
  }
  assert.ok(records.every(row => taskSplits.get(row.task_id) === 'confirmation'));

  const narrowed = await runProcess(process.execPath, [runner, 'calibrate', '--campaign', `${campaign}-narrow`, '--tasks', 'correctness_repair/*', '--dry-run'], {
    cwd: repo, env: { ...process.env, BENCH_CLAUDE: fake }, input: '', deadlineSeconds: 30
  });
  assert.equal(narrowed.code, 1);
  assert.match(narrowed.stderr, /calibration selection must contain exactly 8 tasks; got 2/);
});

test('fake Claude calibration artifacts are accepted by analyze allocate', async t => {
  const dir = await mkdtemp(path.join(scratchRoot, 'bench-allocate-'));
  const campaign = `test-${process.pid}-${Date.now()}`;
  const runner = fileURLToPath(new URL('../run.mjs', import.meta.url));
  const analyzer = fileURLToPath(new URL('../analyze.mjs', import.meta.url));
  const repo = path.resolve(path.dirname(runner), '..');
  const artifacts = path.join(repo, '.tmp', 'bench', campaign);
  t.after(async () => { await retryIO(() => rm(dir, { recursive: true, force: true })); await retryIO(() => rm(artifacts, { recursive: true, force: true })); });
  await installFixtureEngine(artifacts);
  const fake = path.join(dir, 'fake-claude.mjs');
  await writeFile(fake, `import { randomUUID } from 'node:crypto'; if(process.argv.includes('--version')) process.stdout.write('Claude Code 2.1.317\\n'); else { const u={input_tokens:10,cache_read_input_tokens:2,cache_creation_input_tokens:1,output_tokens:4}; process.stdout.write(JSON.stringify({type:'assistant',message:{id:randomUUID(),role:'assistant',usage:u}})+'\\n'+JSON.stringify({type:'result',usage:u})+'\\n'); process.exitCode=1; }`);
  const campaignRun = await runProcess(process.execPath, [runner, 'calibrate', '--campaign', campaign], {
    cwd: repo, env: { ...process.env, BENCH_CLAUDE: fake, GIT_CONFIG_COUNT: '1', GIT_CONFIG_KEY_0: 'core.longpaths', GIT_CONFIG_VALUE_0: 'true' }, input: '', deadlineSeconds: 2400
  });
  assert.equal(campaignRun.code, 0, campaignRun.stderr);
  const records = (await readFile(path.join(artifacts, 'attempts.jsonl'), 'utf8')).split(/\r?\n/).filter(Boolean).map(line => JSON.parse(line));
  assert.equal(records.length, 32);
  for (const row of records) {
    for (const field of ['task_class', 'repetition', 'arm', 'passed', 'verified_wall_time_ms', 'usage', 'n_pairs']) assert.ok(field in row, `${field} missing`);
    for (const field of ['engine_sha', 'harness_sha', 'suite_hash', 'campaign_id', 'n_tasks', 'repetitions', 'n_pairs', 'n_on', 'n_off']) assert.ok(field in row, `${field} provenance missing`);
    assert.equal(row.usage.reconciliation_mismatch, false);
    assert.equal(row.usage.main.input_tokens, 10);
  }
  const allocation = await runProcess(process.execPath, [analyzer, 'allocate', '--campaign', campaign], {
    cwd: repo, env: process.env, input: '', deadlineSeconds: 30
  });
  assert.equal(allocation.code, 0, allocation.stderr);
  assert.equal(JSON.parse(allocation.stdout).provenance.n_pairs, 16);
  assert.ok((await readFile(path.join(artifacts, 'allocation.json'), 'utf8')).includes('variance_components'));
});

test('authentication failure stops calibration and remains retryable on resume', async t => {
  const dir = await mkdtemp(path.join(scratchRoot, 'bench-auth-'));
  const campaign = `test-${process.pid}-${Date.now()}`;
  const runner = fileURLToPath(new URL('../run.mjs', import.meta.url));
  const repo = path.resolve(path.dirname(runner), '..');
  const artifacts = path.join(repo, '.tmp', 'bench', campaign);
  const userConfig = path.join(dir, 'user-config');
  const capture = path.join(dir, 'capture.json');
  t.after(async () => { await retryIO(() => rm(dir, { recursive: true, force: true })); await retryIO(() => rm(artifacts, { recursive: true, force: true })); });
  await installFixtureEngine(artifacts);
  await mkdir(userConfig, { recursive: true });
  await writeFile(path.join(userConfig, 'settings.json'), JSON.stringify({
    enabledPlugins: { 'purser@purser-local': true, 'purser@inline': true, 'other-plugin': true }
  }));
  const fake = path.join(dir, 'fake-claude.mjs');
  await writeFile(fake, `import fs from 'node:fs'; if(process.argv.includes('--version')) process.stdout.write('Claude Code 2.1.317\\n'); else { const args=process.argv.slice(2); const settings=JSON.parse(fs.readFileSync(args[args.indexOf('--settings')+1],'utf8')); const env={CLAUDE_CONFIG_DIR:process.env.CLAUDE_CONFIG_DIR,PURSER_STATE_DIR:process.env.PURSER_STATE_DIR}; const parentPresent=Object.fromEntries(['CLAUDECODE','CLAUDE_CODE_SESSION_ID','CLAUDE_CODE_ENTRYPOINT','CLAUDE_CODE_MESSAGING_TEST','CLAUDE_EFFORT','CLAUDE_PID'].map(key=>[key,key in process.env])); fs.writeFileSync(process.env.BENCH_CAPTURE,JSON.stringify({args,env,parentPresent,anthropicKeyPreserved:process.env.ANTHROPIC_API_KEY==='kept',settings})); process.stdout.write(JSON.stringify({type:'result',is_error:true,terminal_reason:'api_error',result:'Not logged in · Please run /login'})+'\\n'); }`);
  const run = await runProcess(process.execPath, [runner, 'calibrate', '--campaign', campaign], {
    cwd: repo, env: {
      ...process.env, BENCH_CLAUDE: fake, BENCH_CAPTURE: capture, CLAUDE_CONFIG_DIR: userConfig,
      GIT_CONFIG_COUNT: '1', GIT_CONFIG_KEY_0: 'core.longpaths', GIT_CONFIG_VALUE_0: 'true',
      CLAUDECODE: 'parent', CLAUDE_CODE_SESSION_ID: 'parent-session', CLAUDE_CODE_ENTRYPOINT: 'parent-cli',
      CLAUDE_CODE_MESSAGING_TEST: 'parent-message', CLAUDE_EFFORT: 'medium', CLAUDE_PID: '987', ANTHROPIC_API_KEY: 'kept'
    }, input: '', deadlineSeconds: 2400
  });
  assert.equal(run.code, 1);
  assert.match(run.stderr, /Claude authentication failed.*Run \/login.*resume the campaign/);
  const launch = JSON.parse(await readFile(capture, 'utf8'));
  assert.equal(launch.args[launch.args.indexOf('--setting-sources') + 1], 'project,local');
  assert.equal(launch.args.includes(userConfig), false);
  assert.equal(launch.env.CLAUDE_CONFIG_DIR, userConfig);
  for (const key of ['CLAUDECODE', 'CLAUDE_CODE_SESSION_ID', 'CLAUDE_CODE_ENTRYPOINT', 'CLAUDE_CODE_MESSAGING_TEST', 'CLAUDE_EFFORT', 'CLAUDE_PID']) assert.equal(launch.parentPresent[key], false, key);
  assert.equal(launch.anthropicKeyPreserved, true);
  assert.equal(launch.settings.enabledPlugins['purser@purser-local'], false);
  assert.equal(typeof launch.settings.enabledPlugins['purser@inline'], 'boolean');
  const attemptFiles = (await readdir(artifacts)).filter(file => file !== 'campaign.json' && file.endsWith('.json'));
  const records = await Promise.all(attemptFiles.map(async file => JSON.parse(await readFile(path.join(artifacts, file), 'utf8'))));
  assert.equal(records.length, 1);
  assert.equal(records[0].attempt_status, 'failed');
  assert.equal(records[0].failure_reason, 'auth');
  assert.equal(launch.settings.enabledPlugins['purser@inline'], records[0].arm === 'on');
  assert.equal((await terminalAttempts(path.join(artifacts, 'attempts.jsonl'))).size, 0);
});

test('permission denials stop calibration without a terminal ledger record', async t => {
  const dir = await mkdtemp(path.join(scratchRoot, 'bench-permission-'));
  const campaign = `test-${process.pid}-${Date.now()}`;
  const runner = fileURLToPath(new URL('../run.mjs', import.meta.url));
  const repo = path.resolve(path.dirname(runner), '..');
  const artifacts = path.join(repo, '.tmp', 'bench', campaign);
  const fake = path.join(dir, 'fake-claude.mjs');
  t.after(async () => { await retryIO(() => rm(dir, { recursive: true, force: true })); await retryIO(() => rm(artifacts, { recursive: true, force: true })); });
  await installFixtureEngine(artifacts);
  await writeFile(fake, `if(process.argv.includes('--version')) process.stdout.write('Claude Code 2.1.317\\n'); else process.stdout.write(JSON.stringify({type:'result',permission_denials:[{tool_name:'Edit'}]})+'\\n');`);
  const run = await runProcess(process.execPath, [runner, 'calibrate', '--campaign', campaign], {
    cwd: repo, env: { ...process.env, BENCH_CLAUDE: fake, GIT_CONFIG_COUNT: '1', GIT_CONFIG_KEY_0: 'core.longpaths', GIT_CONFIG_VALUE_0: 'true' },
    input: '', deadlineSeconds: 2400
  });
  assert.equal(run.code, 1);
  assert.match(run.stderr, /Claude permission denied.*before resuming the campaign/);
  const attemptFiles = (await readdir(artifacts)).filter(file => file !== 'campaign.json' && file.endsWith('.json'));
  assert.equal(attemptFiles.length, 1);
  const record = JSON.parse(await readFile(path.join(artifacts, attemptFiles[0]), 'utf8'));
  assert.equal(record.attempt_status, 'failed');
  assert.equal(record.failure_reason, 'permission');
  assert.deepEqual(JSON.parse(await readFile(path.join(artifacts, 'config', attemptFiles[0].replace(/\.json$/, ''), 'settings.json'), 'utf8')).permissions, { defaultMode: 'bypassPermissions' });
  assert.equal((await terminalAttempts(path.join(artifacts, 'attempts.jsonl'))).size, 0);
});

test('Purser engine init failure aborts calibration without a terminal record', async t => {
  const dir = await mkdtemp(path.join(scratchRoot, 'bench-engine-failure-'));
  const campaign = `test-${process.pid}-${Date.now()}`;
  const runner = fileURLToPath(new URL('../run.mjs', import.meta.url));
  const repo = path.resolve(path.dirname(runner), '..');
  const artifacts = path.join(repo, '.tmp', 'bench', campaign);
  t.after(async () => { await retryIO(() => rm(dir, { recursive: true, force: true })); await retryIO(() => rm(artifacts, { recursive: true, force: true })); });
  await installFixtureEngine(artifacts);
  const fake = path.join(dir, 'fake-claude.mjs');
  await writeFile(fake, `import fs from 'node:fs'; if(process.argv.includes('--version')) process.stdout.write('Claude Code 2.1.317\\n'); else { const a=process.argv.slice(2), s=JSON.parse(fs.readFileSync(a[a.indexOf('--settings')+1])); if(s.enabledPlugins['purser@inline']) process.stdout.write(JSON.stringify({type:'system',subtype:'init',mcp_servers:[{name:'plugin:purser:codex-main',status:'failed'}]})+'\\n'); else process.stdout.write(JSON.stringify({type:'result'})+'\\n'); process.exitCode=1; }`);
  const run = await runProcess(process.execPath, [runner, 'calibrate', '--campaign', campaign], {
    cwd: repo, env: { ...process.env, BENCH_CLAUDE: fake, GIT_CONFIG_COUNT: '1', GIT_CONFIG_KEY_0: 'core.longpaths', GIT_CONFIG_VALUE_0: 'true' }, input: '', deadlineSeconds: 2400
  });
  assert.equal(run.code, 1);
  assert.match(run.stderr, /Purser engine failed to connect/);
  const files = (await readdir(artifacts)).filter(file => file.endsWith('.json') && file !== 'campaign.json');
  const records = await Promise.all(files.map(async file => JSON.parse(await readFile(path.join(artifacts, file), 'utf8'))));
  const failed = records.find(record => record.failure_reason === 'engine');
  assert.ok(failed);
  assert.equal(failed.attempt_status, 'failed');
  const ledger = (await readFile(path.join(artifacts, 'attempts.jsonl'), 'utf8').catch(error => error.code === 'ENOENT' ? '' : Promise.reject(error))).split(/\r?\n/).filter(Boolean).map(JSON.parse);
  assert.ok(ledger.every(record => record.failure_reason !== 'engine'));
  assert.equal((await terminalAttempts(path.join(artifacts, 'attempts.jsonl'))).has(JSON.stringify([failed.task_id, failed.repetition, failed.arm])), false);
});

test('Purser contamination in off init aborts calibration without a terminal record', async t => {
  const dir = await mkdtemp(path.join(scratchRoot, 'bench-contamination-'));
  const campaign = `test-${process.pid}-${Date.now()}`;
  const runner = fileURLToPath(new URL('../run.mjs', import.meta.url));
  const repo = path.resolve(path.dirname(runner), '..');
  const artifacts = path.join(repo, '.tmp', 'bench', campaign);
  t.after(async () => { await retryIO(() => rm(dir, { recursive: true, force: true })); await retryIO(() => rm(artifacts, { recursive: true, force: true })); });
  await installFixtureEngine(artifacts);
  const fake = path.join(dir, 'fake-claude.mjs');
  await writeFile(fake, `import fs from 'node:fs'; if(process.argv.includes('--version')) process.stdout.write('Claude Code 2.1.317\\n'); else { const a=process.argv.slice(2), s=JSON.parse(fs.readFileSync(a[a.indexOf('--settings')+1])); if(!s.enabledPlugins['purser@inline']) process.stdout.write(JSON.stringify({type:'system',subtype:'init',plugins:[{name:'plugin:purser:codex-main'}]})+'\\n'); else process.stdout.write(JSON.stringify({type:'result'})+'\\n'); process.exitCode=1; }`);
  const run = await runProcess(process.execPath, [runner, 'calibrate', '--campaign', campaign], {
    cwd: repo, env: { ...process.env, BENCH_CLAUDE: fake, GIT_CONFIG_COUNT: '1', GIT_CONFIG_KEY_0: 'core.longpaths', GIT_CONFIG_VALUE_0: 'true' }, input: '', deadlineSeconds: 2400
  });
  assert.equal(run.code, 1);
  assert.match(run.stderr, /Purser plugin contamination detected/);
  const files = (await readdir(artifacts)).filter(file => file.endsWith('.json') && file !== 'campaign.json');
  const records = await Promise.all(files.map(async file => JSON.parse(await readFile(path.join(artifacts, file), 'utf8'))));
  const failed = records.find(record => record.failure_reason === 'contamination');
  assert.ok(failed);
  assert.equal(failed.attempt_status, 'failed');
  const ledger = (await readFile(path.join(artifacts, 'attempts.jsonl'), 'utf8').catch(error => error.code === 'ENOENT' ? '' : Promise.reject(error))).split(/\r?\n/).filter(Boolean).map(JSON.parse);
  assert.ok(ledger.every(record => record.failure_reason !== 'contamination'));
  assert.equal((await terminalAttempts(path.join(artifacts, 'attempts.jsonl'))).has(JSON.stringify([failed.task_id, failed.repetition, failed.arm])), false);
});

test('deadline kills the process tree', async t => {
  const dir = await mkdtemp(path.join(scratchRoot, 'bench-timeout-'));
  t.after(() => retryIO(() => rm(dir, { recursive: true, force: true })));
  const marker = path.join(dir, 'orphan.txt');
  const childCode = `setTimeout(()=>require('fs').writeFileSync(${JSON.stringify(marker)},'alive'),1200)`;
  const parentCode = `require('child_process').spawn(process.execPath,['-e',${JSON.stringify(childCode)}],{stdio:'ignore'}); setTimeout(()=>{},10000)`;
  const result = await runProcess(process.execPath, ['-e', parentCode], { cwd: dir, env: process.env, input: '', deadlineSeconds: 0.15 });
  assert.equal(result.timedOut, true);
  await new Promise(resolve => setTimeout(resolve, 1400));
  await assert.rejects(readFile(marker));
});

