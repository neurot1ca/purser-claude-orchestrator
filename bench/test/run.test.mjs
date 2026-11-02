import test, { after } from 'node:test';
import assert from 'node:assert/strict';
import { appendFile, mkdir, mkdtemp, rm, rmdir, readFile, writeFile } from 'node:fs/promises';
import { execFileSync, spawn } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { armSettings, campaignMetadata, ensureEngine, grade, retryIO, runProcess, terminalAttempts, usageFromTranscript } from '../run.mjs';

const scratchRoot = fileURLToPath(new URL('../../.tmp/bench-test/', import.meta.url));
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

test('engine checkout populates a no-checkout clone whose HEAD is already pinned', async t => {
  const repo = fileURLToPath(new URL('../../', import.meta.url));
  const scratch = await mkdtemp(path.join(scratchRoot, 'bench-engine-'));
  t.after(() => retryIO(() => rm(scratch, { recursive: true, force: true })));
  const engine = path.join(scratch, 'engine');
  const { pinned_commit } = JSON.parse(await readFile(path.join(repo, 'bench', 'protocol.json'), 'utf8'));
  const options = { cwd: repo, shell: false, windowsHide: true, encoding: 'utf8' };
  execFileSync('git', ['clone', '--quiet', '--no-checkout', repo, engine], options);
  execFileSync('git', ['-C', engine, 'update-ref', '--no-deref', 'HEAD', pinned_commit], options);
  const plugin = path.join(engine, '.claude-plugin', 'plugin.json');
  await assert.rejects(readFile(plugin), { code: 'ENOENT' });
  const result = await ensureEngine(engine, false);
  assert.equal(result.sha, pinned_commit);
  assert.equal((await readFile(path.join(engine, '.git', 'HEAD'), 'utf8')).trim(), pinned_commit);
  assert.deepEqual(JSON.parse(await readFile(plugin, 'utf8')), JSON.parse(execFileSync('git', ['show', `${pinned_commit}:.claude-plugin/plugin.json`], options)));
});

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

test('arm settings differ only at the Purser plugin switch', () => {
  const on = armSettings('on'), off = armSettings('off');
  assert.deepEqual(Object.keys(on), Object.keys(off));
  const key = 'purser@inline';
  assert.deepEqual(Object.keys(on.enabledPlugins), [key]);
  assert.equal(on.enabledPlugins[key], true);
  assert.equal(off.enabledPlugins[key], false);
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
  assert.deepEqual(mismatch.main, expected);
  assert.equal(mismatch.aggregate.output_tokens, 5);
  assert.equal(mismatch.reconciliation_mismatch, true);
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
  const fake = path.join(dir, 'fake-claude.mjs');
  await writeFile(fake, `process.stdout.write('Claude Code 2.1.302\\n');`);
  const result = await new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [runner, 'preflight', '--campaign', campaign, '--dry-run'], { env: { ...process.env, BENCH_CLAUDE: fake }, shell: false, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '', stderr = '';
    child.stdout.setEncoding('utf8').on('data', chunk => { stdout += chunk; });
    child.stderr.setEncoding('utf8').on('data', chunk => { stderr += chunk; });
    child.once('error', reject); child.once('close', code => resolve({ code, stdout, stderr }));
  });
  assert.equal(result.code, 0, result.stderr);
  assert.match(result.stdout, /2\.1\.302/);
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

