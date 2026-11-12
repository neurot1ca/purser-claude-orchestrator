import { spawn } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
import { inflateRawSync } from 'node:zlib';
import { chmod, mkdir, readFile, realpath, readdir, rename, rm, stat, writeFile, appendFile } from 'node:fs/promises';
import { homedir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const PROTOCOL = JSON.parse(await readFile(path.join(ROOT, 'bench/protocol.json'), 'utf8'));
const RETRIES = 6;
const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
const sha = value => createHash('sha256').update(value).digest('hex');
const json = value => JSON.stringify(value, null, 2) + '\n';
const EDIT_DELEGATION_REASON = 'Delegate this code change to the selected coder through the purser:dispatcher agent or codex_start.';
const PURSER_GUIDANCE = 'Purser is installed for this project. Delegate repository code changes (features, fixes, refactors, tests) to the configured coder through the `purser:dispatcher` agent or `codex_start`; Purser\'s refusal messages explain the required first prompt line. Make trivial edits, such as a typo or a one-line change, yourself. Review each result, then accept and apply it with `codex_apply`.';
// initsetup's Codex defaults and explicit Codex reviewer selection (policy reads reviewer.selected).
const ON_ARM_ROLES = `schema_version = 1

[roles]
orchestrator = "claude-code"
coder = "codex"
reviewer = "codex"

[reviewer]
selected = { agent = "codex", model = "gpt-6-luna", effort = "medium" }

[coding]
coder_agent = "codex"
routes = { mechanical = { model = "gpt-6-luna", effort = "high" }, subtle = { model = "gpt-6-luna", effort = "high" }, normal = { model = "gpt-6.1-sol", effort = "medium" }, consequential = { model = "gpt-6.1-sol", effort = "high" }, escalated = { model = "gpt-6.1-sol", effort = "xhigh" }, operation = { model = "gpt-6-luna", effort = "high" }, plan_standard = { model = "gpt-6.1-sol", effort = "high" } }
`;

export async function retryIO(fn, attempts = RETRIES) {
  for (let i = 0; ; i++) {
    try { return await fn(); }
    catch (error) {
      if (!['EBUSY', 'EPERM', 'EACCES'].includes(error.code) || i + 1 >= attempts) throw error;
      await wait(Math.min(50 * 2 ** i, 1000));
    }
  }
}

export function projectKeyForPath(repository, platform = process.platform) {
  const pathApi = platform === 'win32' ? path.win32 : path.posix;
  let canonical = pathApi.normalize(repository);
  if (platform === 'win32' && canonical.startsWith('\\\\?\\UNC\\')) canonical = `\\\\${canonical.slice(8)}`;
  else if (platform === 'win32' && canonical.startsWith('\\\\?\\')) canonical = canonical.slice(4);
  if (canonical !== pathApi.parse(canonical).root) canonical = canonical.replace(/[\\/]+$/, '');
  return sha(platform === 'win32' ? canonical.toLowerCase() : canonical).slice(0, 16);
}

export async function writeOnArmRoles(stateRoot, repository) {
  const canonical = await retryIO(() => realpath(repository)).catch(() => repository);
  const projectKey = projectKeyForPath(canonical);
  const rolesPath = path.join(stateRoot, 'projects', projectKey, 'roles.toml');
  await retryIO(() => mkdir(path.dirname(rolesPath), { recursive: true }));
  await retryIO(() => writeFile(rolesPath, ON_ARM_ROLES));
  return { projectKey, rolesPath };
}

export async function hasPurserGuidance(repository, session, streamEvents, configDir = path.resolve(process.env.CLAUDE_CONFIG_DIR || path.join(homedir(), '.claude'))) {
  const projectsDir = path.join(configDir, 'projects');
  const projectDir = repository.replace(/[^A-Za-z0-9]/g, '-');
  const filename = `${session}.jsonl`;
  async function sessionLogHasGuidance(file) {
    const log = await retryIO(() => readFile(file, 'utf8')).catch(() => '');
    return log.split(/\r?\n/).filter(Boolean).some(line => {
      let entry;
      try { entry = JSON.parse(line); } catch { return false; }
      const attachment = entry?.type === 'attachment' ? entry.attachment : null;
      return attachment?.type === 'hook_additional_context' && attachment.hookEvent === 'SessionStart' &&
        Array.isArray(attachment.content) && attachment.content.some(content => typeof content === 'string' && content.includes(PURSER_GUIDANCE));
    });
  }
  if (await sessionLogHasGuidance(path.join(projectsDir, projectDir, filename))) return true;
  // Claude can shorten or otherwise canonicalize the project directory name.
  for (const entry of await retryIO(() => readdir(projectsDir, { withFileTypes: true })).catch(() => [])) {
    if (entry.isDirectory() && entry.name !== projectDir && await sessionLogHasGuidance(path.join(projectsDir, entry.name, filename))) return true;
  }
  return streamEvents.some(event => event?.type === 'system' && event.subtype === 'hook_response' &&
    event.hook_event === 'SessionStart' && typeof event.stdout === 'string' && event.stdout.includes(PURSER_GUIDANCE));
}

export async function cleanupRunDir(dir, { durationMs = 60_000, remove = () => rm(dir, { recursive: true, force: true }) } = {}) {
  const deadline = Date.now() + durationMs;
  let delay = 50, lastError;
  while (true) {
    try { await remove(); return { pending: false }; }
    catch (error) {
      lastError = error;
      if (!['EBUSY', 'EPERM', 'EACCES'].includes(error.code) || Date.now() >= deadline) {
        console.warn(`Warning: cleanup pending for ${dir}: ${error.message}`);
        return { pending: true, error: lastError };
      }
      await wait(Math.min(delay, Math.max(0, deadline - Date.now())));
      delay = Math.min(delay * 2, 1000);
    }
  }
}

export function armSettings(arm, disabledPlugins = []) {
  if (!['on', 'off'].includes(arm)) throw new Error(`invalid arm: ${arm}`);
  return { permissions: { defaultMode: 'bypassPermissions' }, enabledPlugins: { ...Object.fromEntries(disabledPlugins.map(key => [key, false])), [PROTOCOL.pinned_engine.treatment_plugin_key]: arm === 'on' } };
}

export function childEnvironment(env, stateDir) {
  return { ...Object.fromEntries(Object.entries(env).filter(([key]) => key !== 'CLAUDECODE' && !key.startsWith('CLAUDE_CODE_') && key !== 'CLAUDE_EFFORT' && key !== 'CLAUDE_PID')), PURSER_STATE_DIR: stateDir, PURSER_DISABLE_CATALOG_REFRESH: '1' };
}

function gitEnvironment(env) {
  const count = Number(env.GIT_CONFIG_COUNT || 0);
  return { ...env, GIT_CONFIG_COUNT: String(count + 1), [`GIT_CONFIG_KEY_${count}`]: 'core.longpaths', [`GIT_CONFIG_VALUE_${count}`]: 'true' };
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

export function permissionFailure(events, arm) {
  const editTools = new Set(['Edit', 'Write', 'MultiEdit', 'NotebookEdit']);
  const toolResults = events.flatMap(event => event.type === 'user' && Array.isArray(event.message?.content)
    ? event.message.content
    : []);
  return events.some(event => event.type === 'result' && Array.isArray(event.permission_denials) && event.permission_denials.some(denial => {
    if (arm !== 'on' || !editTools.has(denial?.tool_name) || !denial?.tool_use_id) return true;
    return !toolResults.some(result => {
      if (result.type !== 'tool_result' || result.is_error !== true || result.tool_use_id !== denial.tool_use_id) return false;
      const content = typeof result.content === 'string' ? result.content : Array.isArray(result.content) &&
        result.content.every(block => block?.type === 'text' && typeof block.text === 'string')
        ? result.content.map(block => block.text).join('')
        : undefined;
      return content === EDIT_DELEGATION_REASON;
    });
  }));
}

function pluginContamination(events, arm) {
  const purser = value => typeof value === 'string' ? value.startsWith('plugin:purser:') :
    value && typeof value === 'object' && [value.name, value.id, value.key].some(name => typeof name === 'string' && name.startsWith('plugin:purser:'));
  for (const event of events) {
    for (const payload of [event, event.data, event.message].filter(value => value && typeof value === 'object')) {
      const servers = Array.isArray(payload.mcp_servers) ? payload.mcp_servers : [];
      const plugins = Array.isArray(payload.plugins) ? payload.plugins : [];
      if (arm === 'off' && [...servers, ...plugins].some(purser)) return 'contamination';
      if (arm === 'on' && servers.some(server => purser(server) && server.status !== 'connected')) return 'engine';
    }
  }
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
      if (!side) {
        if (!aggregate) aggregate = { ...usage };
        else for (const category of Object.keys(main)) {
          aggregate[category] = Number.isFinite(aggregate[category]) && Number.isFinite(usage[category])
            ? aggregate[category] + usage[category]
            : Number.NaN;
        }
      }
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

function accountTranscript(transcript) {
  let usage = { main: 'unknown', workers: 'unknown' }, accountingError;
  try {
    const parsed = [];
    for (const line of transcript.split(/\r?\n/).filter(Boolean)) {
      try { parsed.push(JSON.parse(line)); }
      catch { throw new Error('malformed stream-json transcript'); }
    }
    const result = usageFromTranscript(parsed);
    usage = { main: result.main, aggregate: result.aggregate, reconciliation_mismatch: result.reconciliation_mismatch, workers: result.workers.length ? result.workers : 'unknown' };
    if (result.reconciliation_mismatch === true) accountingError = 'main transcript usage does not match aggregate result usage';
  } catch (error) { accountingError = error.message; }
  return { usage, accounting_error: accountingError };
}

export async function reaccountCampaign(campaignDir) {
  const summary = { ok: true, updated: 0, unchanged: 0, skipped: 0, ledger_updated: 0 };
  const accounted = new Map();
  const files = (await readdir(campaignDir)).filter(file => file.endsWith('.json') && file !== 'campaign.json').sort();
  for (const name of files) {
    const file = path.join(campaignDir, name);
    let record;
    try { record = JSON.parse(await retryIO(() => readFile(file, 'utf8'))); }
    catch (error) { console.error(`reaccount skipped ${name}: cannot read run JSON: ${error.message}`); summary.skipped++; continue; }
    if (!record || typeof record.transcript_sha !== 'string') continue;
    const runId = path.basename(name, '.json');
    const transcriptPath = path.join(campaignDir, 'transcripts', runId, 'stdout.jsonl');
    let transcript;
    try { transcript = await retryIO(() => readFile(transcriptPath)); }
    catch (error) { console.error(`reaccount skipped ${name}: transcript missing or unreadable: ${error.message}`); summary.skipped++; continue; }
    if (sha(transcript) !== record.transcript_sha) {
      console.error(`reaccount skipped ${name}: transcript sha256 mismatch`); summary.skipped++; continue;
    }
    const accounting = accountTranscript(transcript.toString('utf8'));
    const key = JSON.stringify([record.session_id, record.task_id, record.repetition, record.arm]);
    if ([record.session_id, record.task_id, record.repetition, record.arm].every(value => value != null)) accounted.set(key, accounting);
    if (JSON.stringify(record.usage) === JSON.stringify(accounting.usage) && record.accounting_error === accounting.accounting_error) {
      summary.unchanged++; continue;
    }
    record.usage = accounting.usage;
    record.accounting_error = accounting.accounting_error;
    const temporary = path.join(campaignDir, `${name}.${randomUUID()}.tmp`);
    try {
      await retryIO(() => writeFile(temporary, json(record)));
      await retryIO(() => rename(temporary, file));
      summary.updated++;
    } catch (error) {
      accounted.delete(key);
      await retryIO(() => rm(temporary, { force: true })).catch(() => {});
      console.error(`reaccount skipped ${name}: cannot rewrite run JSON: ${error.message}`); summary.skipped++;
    }
  }
  const ledgerPath = path.join(campaignDir, 'attempts.jsonl');
  const temporary = path.join(campaignDir, `attempts.jsonl.${randomUUID()}.tmp`);
  try {
    const ledger = await retryIO(() => readFile(ledgerPath)).catch(error => {
      if (error.code === 'ENOENT') return Buffer.alloc(0);
      throw error;
    });
    const lines = [];
    let updated = 0;
    for (let start = 0; start < ledger.length;) {
      const newline = ledger.indexOf(10, start);
      const end = newline === -1 ? ledger.length : newline + 1;
      let line = ledger.subarray(start, end);
      let record;
      try { record = JSON.parse(line.toString('utf8')); } catch {}
      if (record && typeof record === 'object') {
        const accounting = accounted.get(JSON.stringify([record.session_id, record.task_id, record.repetition, record.arm]));
        if (accounting && (JSON.stringify(record.usage) !== JSON.stringify(accounting.usage) || record.accounting_error !== accounting.accounting_error)) {
          record.usage = accounting.usage;
          record.accounting_error = accounting.accounting_error;
          const ending = newline === -1 ? '' : ledger[newline - 1] === 13 ? '\r\n' : '\n';
          line = Buffer.from(JSON.stringify(record) + ending);
          updated++;
        }
      }
      lines.push(line);
      start = end;
    }
    if (updated) {
      await retryIO(() => writeFile(temporary, Buffer.concat(lines)));
      await retryIO(() => rename(temporary, ledgerPath));
      summary.ledger_updated = updated;
    }
  } catch (error) {
    await retryIO(() => rm(temporary, { force: true })).catch(() => {});
    console.error(`reaccount skipped attempts.jsonl: ${error.message}`); summary.skipped++;
  }
  summary.ok = summary.skipped === 0;
  return summary;
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

export async function runProcess(exe, args, { cwd, env, input, deadlineSeconds, waitForTree = false }) {
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
    child.once('close', async (code, signal) => {
      clearTimeout(timer);
      // On Windows, taskkill can still enumerate descendants after the direct
      // child closes. POSIX children share the detached process group.
      if (waitForTree) await killTree();
      resolve({ code, signal, timedOut, stdout: Buffer.concat(chunks.stdout).toString('utf8'), stderr: Buffer.concat(chunks.stderr).toString('utf8') });
    });
    child.stdin.end(input);
  });
}

async function git(args, cwd = ROOT) {
  const result = await command('git', ['-c', 'core.longpaths=true', ...args], { cwd });
  if (result.code !== 0) throw new Error(`git ${args.join(' ')} failed: ${result.stderr}`);
  return result.stdout.trim();
}

export async function grade(target, m, campaignDir) {
  const verify = path.join(campaignDir, 'grading', `${m.id}-${randomUUID()}`);
  await mkdir(path.dirname(verify), { recursive: true });
  const clone = await command('git', ['-c', 'core.longpaths=true', 'clone', '--quiet', '--no-hardlinks', ROOT, verify]);
  if (clone.code) throw new Error(`grading clone failed: ${clone.stderr}`);
  await git(['checkout', '--detach', m.start_commit], verify);
  const excluded = (m.check.files ?? []).map(file => `:(exclude)${file}`);
  await git(['add', '-A', '-N'], target);
  const diff = await command('git', ['-c', 'core.longpaths=true', 'diff', '--binary', m.start_commit, '--', '.', ...excluded], { cwd: target });
  if (diff.code) throw new Error(`production diff failed: ${diff.stderr}`);
  if (diff.stdout) {
    const applied = await command('git', ['-c', 'core.longpaths=true', 'apply', '--whitespace=nowarn', '-'], { cwd: verify, input: diff.stdout });
    if (applied.code) throw new Error(`production diff rejected: ${applied.stderr}`);
  }
  for (const file of m.check.files ?? []) {
    const shown = await command('git', ['-c', 'core.longpaths=true', 'show', `${m.source_commit}:${file}`], { cwd: ROOT });
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
  if (structural) check = await runProcess(m.check.command[0], m.check.command.slice(1), { cwd: verify, env: gitEnvironment(process.env), input: '', deadlineSeconds: m.deadline_seconds, waitForTree: true });
  await cleanupRunDir(verify);
  return { passed: structural && !check.timedOut && check.code === 0, ...(check.timedOut ? { attempt_status: 'grading_timeout' } : {}), check_exit_code: check.code, check_stdout: check.stdout, check_stderr: check.stderr };
}

export async function ensureEngine(dir, dryRun, { download = downloadArchive } = {}) {
  const pinned = PROTOCOL.pinned_engine;
  if (dryRun) return { commit: pinned.commit, dir, sha: pinned.archive_sha256 };
  const version = pinned.tag.replace(/^v/, '');
  const binary = path.join(dir, 'bin', `purser-hook-${goos()}-${goarch()}${process.platform === 'win32' ? '.exe' : ''}`);
  const validate = async () => {
    const plugin = JSON.parse(await retryIO(() => readFile(path.join(dir, '.claude-plugin', 'plugin.json'), 'utf8')).catch(() => { throw new Error('pinned engine plugin.json is missing'); }));
    if (plugin.version !== version) throw new Error(`pinned engine version mismatch: expected ${version}, got ${plugin.version}`);
    const binaryStat = await retryIO(() => stat(binary)).catch(() => null);
    if (!binaryStat?.isFile()) throw new Error(`pinned engine host binary is missing: ${path.relative(dir, binary)}`);
    if (process.platform !== 'win32' && !(binaryStat.mode & 0o111)) await retryIO(() => chmod(binary, binaryStat.mode | 0o111));
  };
  const marker = path.join(dir, '.bench-engine-sha256');
  if ((await stat(dir).catch(() => null))?.isDirectory()) {
    const installedSha = await retryIO(() => readFile(marker, 'utf8')).catch(() => '');
    if (installedSha.trim() === pinned.archive_sha256) { await validate(); return { commit: pinned.commit, dir, sha: pinned.archive_sha256 }; }
    await retryIO(() => rm(dir, { recursive: true, force: true }));
  }
  const archive = Buffer.from(await download(pinned.archive_url));
  const actualSha = sha(archive);
  if (actualSha !== pinned.archive_sha256) throw new Error(`pinned engine archive sha256 mismatch: expected ${pinned.archive_sha256}, got ${actualSha}`);
  await mkdir(dir, { recursive: true });
  await extractZip(archive, dir);
  await validate();
  await retryIO(() => writeFile(marker, `${actualSha}\n`));
  return { commit: pinned.commit, dir, sha: actualSha };
}

function goos() { return process.platform === 'win32' ? 'windows' : process.platform === 'darwin' ? 'darwin' : process.platform; }
function goarch() { return ({ x64: 'amd64', ia32: '386', arm: 'arm', arm64: 'arm64' })[process.arch] ?? process.arch; }

async function downloadArchive(url) {
  const response = await fetch(url);
  if (!response.ok) throw new Error(`pinned engine download failed: HTTP ${response.status}`);
  return Buffer.from(await response.arrayBuffer());
}

async function extractZip(archive, dest) {
  let eocd = -1;
  for (let i = archive.length - 22; i >= Math.max(0, archive.length - 65557); i--) {
    if (archive.readUInt32LE(i) === 0x06054b50) { eocd = i; break; }
  }
  if (eocd < 0) throw new Error('pinned engine archive is not a valid ZIP');
  const entries = archive.readUInt16LE(eocd + 10), centralOffset = archive.readUInt32LE(eocd + 16);
  let offset = centralOffset;
  for (let i = 0; i < entries; i++) {
    if (archive.readUInt32LE(offset) !== 0x02014b50) throw new Error('pinned engine archive has an invalid ZIP directory');
    const flags = archive.readUInt16LE(offset + 8), method = archive.readUInt16LE(offset + 10), compressedSize = archive.readUInt32LE(offset + 20);
    const nameLength = archive.readUInt16LE(offset + 28), extraLength = archive.readUInt16LE(offset + 30), commentLength = archive.readUInt16LE(offset + 32), localOffset = archive.readUInt32LE(offset + 42);
    const name = archive.subarray(offset + 46, offset + 46 + nameLength).toString(flags & 0x800 ? 'utf8' : 'utf8').replaceAll('\\', '/');
    offset += 46 + nameLength + extraLength + commentLength;
    if (name.endsWith('/')) { await mkdir(path.join(dest, ...name.split('/').filter(Boolean)), { recursive: true }); continue; }
    if (flags & 1) throw new Error('pinned engine archive contains encrypted files');
    if (archive.readUInt32LE(localOffset) !== 0x04034b50) throw new Error('pinned engine archive has an invalid file entry');
    const localNameLength = archive.readUInt16LE(localOffset + 26), localExtraLength = archive.readUInt16LE(localOffset + 28);
    const start = localOffset + 30 + localNameLength + localExtraLength;
    const compressed = archive.subarray(start, start + compressedSize);
    const content = method === 0 ? compressed : method === 8 ? inflateRawSync(compressed) : null;
    if (!content) throw new Error(`unsupported pinned engine ZIP compression: ${method}`);
    const target = path.resolve(dest, ...name.split('/'));
    if (target !== dest && !target.startsWith(dest + path.sep)) throw new Error('pinned engine archive contains an unsafe path');
    await retryIO(() => mkdir(path.dirname(target), { recursive: true }));
    await retryIO(() => writeFile(target, content));
    const mode = archive.readUInt32LE(offset - (46 + nameLength + extraLength + commentLength) + 38) >>> 16;
    if (mode & 0o111) await retryIO(() => chmod(target, mode & 0o777));
  }
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
        if (!pattern || pattern.split(',').some(p => globMatch(rel, p.trim()))) found.push(path.dirname(full));
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
  if (!['preflight', 'calibrate', 'run', 'reaccount', 'validate'].includes(action)) throw new Error('usage: node bench/run.mjs preflight|calibrate|run --campaign <id> [--tasks <glob>] [--reps N] [--dry-run] [--resume] | reaccount --campaign <id> | validate [--tasks <glob>]');
  const opts = { action, reps: action === 'calibrate' ? 2 : 1, dryRun: false };
  for (let i = 0; i < rest.length; i++) {
    const a = rest[i];
    if (a === '--dry-run') opts.dryRun = true;
    else if (a === '--resume') opts.resume = true;
    else if (['--campaign', '--tasks', '--reps'].includes(a)) opts[a.slice(2)] = rest[++i];
    else throw new Error(`unknown argument: ${a}`);
  }
  if (action === 'validate') return opts;
  if (!opts.campaign || opts.campaign.startsWith('.') || !/^[A-Za-z0-9._-]+$/.test(opts.campaign)) throw new Error('--campaign is required and must be a safe id');
  if (action === 'reaccount') return opts;
  opts.reps = Number(opts.reps);
  if (!Number.isInteger(opts.reps) || opts.reps < 1) throw new Error('--reps must be a positive integer');
  return opts;
}

async function preflight(campaignDir, dryRun, requireEngine = false) {
  const claude = await resolveClaude();
  const version = await command(claude.exe, [...claude.prefix, '--version']);
  if (version.code !== 0 || !version.stdout.trim().includes(PROTOCOL.cli.version)) throw new Error(`Claude CLI must be ${PROTOCOL.cli.version}; got ${version.stdout.trim() || version.stderr.trim()}`);
  const engine = await ensureEngine(path.join(campaignDir, 'engine'), dryRun && !requireEngine);
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
    if (['completed', 'failed', 'deadline', 'grading_timeout', 'dry_run'].includes(record.attempt_status) && !['auth', 'permission', 'engine', 'contamination', 'purser_not_initialized'].includes(record.failure_reason)) {
      terminal.add(JSON.stringify([record.task_id, record.repetition, record.arm]));
    }
  }
  return terminal;
}

export function undefinedGoIdentifiers(output) {
  return [...new Set([
    ...[...output.matchAll(/undefined:\s+(?:[A-Za-z_]\w*\.)?([A-Za-z_]\w*)/g)].map(match => match[1]),
    ...[...output.matchAll(/has no field or method\s+([A-Za-z_]\w*)/g)].map(match => match[1]),
    ...[...output.matchAll(/(?:too many|not enough) arguments in call to ([A-Za-z_]\w*)/g)].map(match => match[1])
  ])];
}

export function promptNeedsIdentifiers(identifiers, prompt) {
  return identifiers.filter(symbol => !new RegExp(`\\b${symbol}\\b`).test(prompt));
}

export function goCheckContract(compiled, prompt) {
  const output = `${compiled.stdout}\n${compiled.stderr}`;
  const needed = undefinedGoIdentifiers(output);
  const uncovered = promptNeedsIdentifiers(needed, prompt);
  let detail = uncovered.length ? `prompt omits trusted Go check identifier(s): ${uncovered.join(', ')}` : '';
  const unrecognizedErrors = output.split(/\r?\n/).flatMap(line => {
    if (!line || /^(?:\s|#\s|(?:FAIL|ok)(?:\s|$))/.test(line)) return [];
    const match = line.match(/^(.+?\.go):(\d+)(?::\d+)?:\s*(.*)$/);
    return !undefinedGoIdentifiers(match ? match[3] : line).length ? [line] : [];
  });
  if (compiled.timedOut || (compiled.code !== 0 && (!needed.length || unrecognizedErrors.length))) {
    const diagnostics = unrecognizedErrors.length ? unrecognizedErrors : output.trim().split(/\r?\n/);
    detail = `Go contract compile ${compiled.timedOut ? 'timed out' : `exited ${compiled.code}`}:\n${diagnostics.slice(-20).join('\n')}`;
  }
  return { needed, detail };
}

export async function validateReferences(dirs) {
  const workspace = path.join(ROOT, '.tmp', 'bench', `validate-${randomUUID()}`);
  await mkdir(workspace, { recursive: true });
  let failures = 0;
  try {
    for (const dir of dirs) {
      const m = JSON.parse(await readFile(path.join(dir, 'task.json'), 'utf8'));
      const target = path.join(workspace, m.id);
      let passed = false, detail = '';
      try {
        const clone = await command('git', ['-c', 'core.longpaths=true', 'clone', '--quiet', '--no-hardlinks', ROOT, target]);
        if (clone.code) throw new Error(clone.stderr);
        await git(['checkout', '--quiet', '--detach', m.start_commit], target);
        let contractFailure = false;
        if (m.check.command?.[0] === 'go' && (m.check.files ?? []).length) {
          for (const file of m.check.files) {
            const shown = await command('git', ['-c', 'core.longpaths=true', 'show', `${m.source_commit}:${file}`], { cwd: ROOT });
            if (shown.code) throw new Error(`cannot retrieve trusted check ${file}: ${shown.stderr}`);
            const dest = path.join(target, file);
            await mkdir(path.dirname(dest), { recursive: true });
            await writeFile(dest, shown.stdout);
          }
          const packages = [];
          for (const arg of m.check.command.slice(2)) {
            if (arg.startsWith('-')) break;
            packages.push(arg);
          }
          const compiled = await runProcess('go', ['test', ...packages, '-run=XXX_NONE'], {
            cwd: target, env: gitEnvironment(process.env), input: '', deadlineSeconds: m.deadline_seconds
          });
          const prompt = await readFile(path.join(dir, m.prompt_file), 'utf8');
          const contract = goCheckContract(compiled, prompt);
          const { needed } = contract;
          for (const symbol of needed) console.log(`  needs: ${symbol}`);
          if (contract.detail) {
            contractFailure = true;
            detail = contract.detail;
          }
        } else if (m.check.files?.length) {
          console.log(`NOTE ${m.id}: non-Go trusted checks skipped by prompt contract compile audit`);
        }
        const applied = await command('git', ['-c', 'core.longpaths=true', 'apply', '--whitespace=nowarn', path.join(dir, 'reference.patch')], { cwd: target });
        if (applied.code) throw new Error(`reference patch rejected: ${applied.stderr}`);
        const result = await grade(target, m, workspace);
        passed = result.passed && !contractFailure;
        if (!result.passed) detail = [detail, result.check_stderr, result.check_stdout].filter(Boolean).join('\n').trim() || `check exited ${result.check_exit_code}`;
      } catch (error) { detail = error.message; }
      if (!passed) failures++;
      console.log(`${passed ? 'PASS' : 'FAIL'} ${m.id}${detail ? `: ${detail}` : ''}`);
      await cleanupRunDir(target, { durationMs: 5_000 });
    }
  } finally {
    await cleanupRunDir(workspace, { durationMs: 5_000 });
  }
  console.log(`${dirs.length - failures}/${dirs.length} references passed`);
  return { failures };
}

async function main() {
  const opts = parseArgs(process.argv.slice(2));
  if (opts.action === 'validate') {
    const dirs = await taskDirs(opts.tasks);
    if (!dirs.length) throw new Error('no task manifests matched');
    const result = await validateReferences(dirs);
    process.exitCode = result.failures ? 1 : 0;
    return;
  }
  const campaignBase = path.resolve(ROOT, '.tmp', 'bench');
  const campaignDir = path.resolve(campaignBase, opts.campaign);
  if (path.dirname(campaignDir) !== campaignBase) throw new Error('campaign directory must stay inside .tmp/bench');
  if (opts.action === 'reaccount') {
    const summary = await reaccountCampaign(campaignDir);
    console.log(json(summary));
    if (!summary.ok) process.exitCode = 1;
    return;
  }
  await mkdir(campaignDir, { recursive: true });
  const pf = await preflight(campaignDir, opts.dryRun, opts.action === 'preflight');
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
      const result = await command('git', ['-c', 'core.longpaths=true', 'show', `${t.manifest.source_commit}:${file}`], { cwd: ROOT });
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
    dry_run: opts.dryRun, n_tasks: tasks.length, repetitions: opts.reps, n_pairs: pairs.length, n_on: pairs.length, n_off: pairs.length
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
          await command('git', ['-c', 'core.longpaths=true', 'clone', '--quiet', '--no-hardlinks', ROOT, target]).then(r => { if (r.code) throw Error(r.stderr); });
          await git(['checkout', '--detach', m.start_commit], target);
          await git(['clean', '-ffdqx'], target);
        }
        if (arm === 'on' && !opts.dryRun) await writeOnArmRoles(stateDir, target);
        const prompt = await readFile(path.join(task.dir, m.prompt_file));
        const session = randomUUID();
        const args = [...pf.claude.prefix, '-p', '--model', metadata.model, '--effort', metadata.effort, '--session-id', session, '--setting-sources', 'project,local', '--settings', configFile, '--plugin-dir', pf.engine.dir, '--output-format', 'stream-json', '--verbose', '--include-hook-events'];
        const started = Date.now();
        let run = { code: 0, timedOut: false, stdout: '', stderr: '' }, outcome = { passed: null };
        if (!opts.dryRun) run = await runProcess(pf.claude.exe, args, { cwd: target, input: prompt, deadlineSeconds: m.deadline_seconds, env: childEnvironment(process.env, stateDir), waitForTree: !process.env.BENCH_CLAUDE });
        await writeFile(path.join(transcriptDir, 'stdout.jsonl'), run.stdout);
        await writeFile(path.join(transcriptDir, 'stderr.log'), run.stderr);
        const transcript = await retryIO(() => readFile(path.join(transcriptDir, 'stdout.jsonl'), 'utf8'));
        let parsed = [], transcriptError;
        for (const line of transcript.split(/\r?\n/).filter(Boolean)) {
          try { parsed.push(JSON.parse(line)); }
          catch { transcriptError = 'malformed stream-json transcript'; break; }
        }
        const isAuthFailure = authFailure(parsed);
        const isPermissionFailure = permissionFailure(parsed, arm);
        const pluginFailure = pluginContamination(parsed, arm);
        const purserNotInitialized = arm === 'on' && !opts.dryRun && !isAuthFailure && !isPermissionFailure && !pluginFailure && !await hasPurserGuidance(target, session, parsed);
        const accounting = transcriptError ? { usage: { main: 'unknown', workers: 'unknown' }, accounting_error: transcriptError } : accountTranscript(transcript);
        const { usage } = accounting, accountingError = accounting.accounting_error;
        if (!opts.dryRun && !isAuthFailure && !isPermissionFailure && !pluginFailure && !purserNotInitialized && !run.timedOut && run.code === 0) outcome = await grade(target, m, campaignDir);
        // The contract measures launch through independent grading.
        const elapsed = Date.now() - started;
        const isEnvironmentFailure = isAuthFailure || isPermissionFailure || pluginFailure || purserNotInitialized;
        const failureReason = isAuthFailure ? 'auth' : isPermissionFailure ? 'permission' : pluginFailure || (purserNotInitialized ? 'purser_not_initialized' : undefined);
        const result = { ...metadata, n_tasks: tasks.length, repetitions: opts.reps, n_pairs: pairs.length, n_on: pairs.length, n_off: pairs.length, task_id: m.id, task_class: m.class, repetition: rep + 1, arm, order, session_id: session, attempt_status: isEnvironmentFailure ? 'failed' : opts.dryRun ? 'dry_run' : run.timedOut ? 'deadline' : run.code === 0 ? 'completed' : 'failed', ...(failureReason ? { failure_reason: failureReason } : {}), exit_code: run.code, timed_out: run.timedOut, elapsed_ms: elapsed, verified_wall_time_ms: outcome.passed === true ? elapsed : m.deadline_seconds * 1000, usage, accounting_error: accountingError, prompt_sha: sha(prompt), settings_sha: sha(await readFile(configFile)), transcript_sha: sha(transcript), binary_sha: sha(await readFile(pf.claude.hashPath)), source_commit: m.source_commit, start_commit: m.start_commit, repository_head: repoHash, ...outcome, worker_usage: usage.workers };
        const runDirRemoval = opts.dryRun ? { pending: false } : await cleanupRunDir(runDir);
        if (runDirRemoval.pending) result.cleanup_pending = true;
        await writeFile(path.join(campaignDir, `${runId}.json`), json(result));
        // Environment failures stay inspectable but must not enter the terminal analysis ledger.
        if (!isEnvironmentFailure) await appendFile(ledger, '\n' + JSON.stringify(result) + '\n');
        if (isAuthFailure) throw new Error('Claude authentication failed (Not logged in). Run /login in the default Claude config, then resume the campaign.');
        if (isPermissionFailure) throw new Error('Claude permission denied one or more tool requests. Check the per-run permission settings before resuming the campaign.');
        if (pluginFailure === 'engine') throw new Error('Purser engine failed to connect in the on run. Check the pinned engine before resuming the campaign.');
        if (pluginFailure === 'contamination') throw new Error('Purser plugin contamination detected in the off run. Check the per-run plugin settings before resuming the campaign.');
        if (purserNotInitialized) throw new Error('Purser SessionStart initialization guidance was missing from the Claude session log and hook stream; the run was excluded from analysis.');
      }
    }
  }
  // Retry any run directories that were still held after their bounded wait.
  for (const parent of ['targets', 'grading']) {
    for (const entry of await readdir(path.join(campaignDir, parent), { withFileTypes: true }).catch(() => [])) {
      if (!entry.isDirectory()) continue;
      const dir = path.join(campaignDir, parent, entry.name);
      const cleanup = await cleanupRunDir(dir, { durationMs: 5_000 });
      if (parent === 'targets' && !cleanup.pending) {
        for (const artifact of await readdir(campaignDir)) {
          if (!artifact.endsWith('.json') || artifact === 'campaign.json') continue;
          const file = path.join(campaignDir, artifact);
          const record = JSON.parse(await readFile(file, 'utf8').catch(() => 'null'));
          if (record?.cleanup_pending && artifact.startsWith(entry.name)) {
            record.cleanup_pending = false;
            await writeFile(file, json(record));
          }
        }
      }
    }
  }
  console.log(json({ ok: true, campaign_id: opts.campaign, tasks: tasks.length, pairs: pairs.length, seed }));
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch(error => { console.error(error.stack || error.message); process.exitCode = 1; });
}
