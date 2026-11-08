import { readFile, readdir } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const categories = ['input_tokens', 'cache_read_input_tokens', 'cache_creation_input_tokens', 'output_tokens'];
const isSide = event => event.isSidechain === true || event.sidechain === true || event.message?.isSidechain === true ||
  event.parent_tool_use_id != null || event.message?.parent_tool_use_id != null ||
  (event.session_id && event.main_session_id && event.session_id !== event.main_session_id);
const tokens = usage => usage?.complete !== false && categories.every(key => Number.isFinite(usage?.[key]))
  ? Object.fromEntries(categories.map(key => [key, usage[key]])) : null;

function emptyArm() {
  return { attempts: 0, main_requests: 0, raw_main_tokens: 0, weighted_main_cost: 0, dispatcher_calls: 0, codex_start_calls: 0,
    dispatcher_tokens: 0, durable_codex_tokens: 0, tools: {}, gaps: new Set(), mainKnown: true, dispatchKnown: true, codexKnown: true, flowKnown: true };
}

function addUsage(out, usage, multiplier = 1) {
  for (const key of categories) out[key] += usage[key] * multiplier;
}

function weightedCost(usage) {
  const oneHour = usage.cache_creation?.ephemeral_1h_input_tokens ?? usage.cache_creation_1h_input_tokens ?? 0;
  const cacheWrite = usage.cache_creation_input_tokens ?? usage.cache_creation?.input_tokens ?? 0;
  return usage.input_tokens + usage.cache_read_input_tokens * 0.1 + Math.max(0, cacheWrite - oneHour) * 1.25 + oneHour * 2 + usage.output_tokens * 5;
}

export function summarizeAttempt(events, result, arm, recordedWorkers = [result?.worker_usage, result?.usage?.workers].find(Array.isArray)) {
  const requests = new Map();
  const dispatcherUsage = new Map(), durableUsage = new Map();
  let oneHourCacheWrite = 0;
  for (const event of events) {
    const message = event.message ?? event;
    const usage = message.usage ?? event.usage;
    const side = isSide(event);
    if (usage && side) {
      const provider = String(usage.provider ?? message.provider ?? event.provider ?? '').toLowerCase();
      const role = String(message.role ?? event.role ?? '').toLowerCase();
      const model = String(message.model ?? event.model ?? '').toLowerCase();
      const id = message.id ?? message.message_id ?? event.request_id ?? Symbol();
      const target = /codex|openai/.test(`${provider} ${role} ${model}`) ? durableUsage : dispatcherUsage;
      const existing = target.get(id) ?? {};
      for (const key of categories) if (Number.isFinite(usage[key])) existing[key] = Math.max(existing[key] ?? usage[key], usage[key]);
      if (typeof usage.complete === 'boolean') existing.complete = usage.complete;
      target.set(id, existing);
      continue;
    }
    if (event.type !== 'assistant' && message.role !== 'assistant') continue;
    const id = message.id ?? message.message_id;
    if (!id || side) continue;
    const existing = requests.get(id) ?? { usage: Object.fromEntries(categories.map(key => [key, 0])), tools: new Set(), calls: new Map(), blocks: new Map(), oneHourCacheWrite: 0 };
    if (usage) for (const key of categories) if (Number.isFinite(usage[key])) existing.usage[key] = Math.max(existing.usage[key], usage[key]);
    const oneHour = usage?.cache_creation?.ephemeral_1h_input_tokens ?? usage?.cache_creation_1h_input_tokens;
    if (Number.isFinite(oneHour)) existing.oneHourCacheWrite = Math.max(existing.oneHourCacheWrite, oneHour);
    const content = message.content ?? event.content ?? [];
    for (const [index, block] of (Array.isArray(content) ? content : []).entries()) {
      if (block.type !== 'tool_use' || typeof block.name !== 'string') continue;
      existing.tools.add(block.name);
      existing.calls.set(block.id ?? `${block.name}:${index}`, block.name);
      existing.blocks.set(block.id ?? `${block.name}:${index}`, block);
    }
    requests.set(id, existing);
  }
  const seenMain = new Set();
  // Main requests are unique within an attempt; merge repeated stream snapshots by message id.
  for (const id of requests.keys()) seenMain.add(id);
  const main = Object.fromEntries(categories.map(key => [key, 0]));
  for (const request of requests.values()) {
    addUsage(main, request.usage);
    oneHourCacheWrite += request.oneHourCacheWrite;
    const combination = [...request.tools].sort().join(' + ') || '(no tools)';
    arm.tools[combination] = (arm.tools[combination] ?? 0) + 1;
  }
  const raw = result?.usage?.main && result.usage.main !== 'unknown' && categories.every(key => Number.isFinite(result.usage.main[key]))
    ? categories.reduce((sum, key) => sum + result.usage.main[key], 0) : null;
  const mainWeighted = raw == null ? null : weightedCost(result.usage.main) + oneHourCacheWrite * 0.75;
  arm.main_requests += seenMain.size;
  if (!seenMain.size) { arm.flowKnown = false; arm.gaps.add(`main request events missing for ${result?.task_id ?? 'unknown task'}`); }
  if (raw == null) arm.mainKnown = false;
  else arm.raw_main_tokens += raw;
  if (mainWeighted == null) arm.mainKnown = false;
  else arm.weighted_main_cost += mainWeighted;
  const calls = [...requests.values()].flatMap(request => [...request.calls].map(([id, name]) => ({ id, name })));
  // Agent/Task tools carry their subtype in the corresponding content block input.
  const toolBlocks = [...requests.values()].flatMap(request => [...request.blocks.values()]);
  const dispatcherCount = toolBlocks.filter(block => /^(agent|task)$/i.test(block.name) && /dispatcher/i.test(String(block.input?.subagent_type ?? ''))).length;
  const codexStartCount = calls.filter(({ name }) => /codex[_-]start/i.test(name)).length;
  arm.dispatcher_calls += dispatcherCount;
  arm.codex_start_calls += codexStartCount;
  const claudeWorkers = (recordedWorkers ?? []).filter(worker => /claude|anthropic/i.test(`${worker.provider ?? ''} ${worker.model ?? ''}`));
  const codexWorkers = (recordedWorkers ?? []).filter(worker => /codex|openai/i.test(`${worker.provider ?? ''} ${worker.model ?? ''}`));
  // Recorded provider rows take precedence; transcript snapshots are a fallback, never an addition.
  const dispatcherRows = dispatcherCount > 0 && claudeWorkers.length ? claudeWorkers.map(worker => worker.usage) : [...dispatcherUsage.values()];
  const durableRows = codexWorkers.length ? codexWorkers.map(worker => worker.usage) : [...durableUsage.values()];
  const sawDispatcherUsage = dispatcherRows.length > 0, dispatcherComplete = dispatcherRows.every(usage => tokens(usage) !== null);
  const sawDurable = durableRows.length > 0, durableComplete = durableRows.every(usage => tokens(usage) !== null);
  if (sawDispatcherUsage && dispatcherComplete) arm.dispatcher_tokens += dispatcherRows.reduce((sum, usage) => sum + categories.reduce((total, key) => total + usage[key], 0), 0);
  if (!result?.usage?.main || result.usage.main === 'unknown') { arm.gaps.add(`main provider usage missing for ${result?.task_id ?? 'unknown task'}`); arm.mainKnown = false; }
  if (!sawDispatcherUsage && dispatcherCount > 0) { arm.gaps.add(`dispatcher token usage missing for ${result?.task_id ?? 'unknown task'}`); arm.dispatchKnown = false; }
  else if (!dispatcherComplete) { arm.gaps.add(`dispatcher token usage incomplete for ${result?.task_id ?? 'unknown task'}`); arm.dispatchKnown = false; }
  if (sawDurable && durableComplete) arm.durable_codex_tokens += durableRows.reduce((sum, usage) => sum + categories.reduce((total, key) => total + usage[key], 0), 0);
  else if (!sawDurable && (codexStartCount > 0 || dispatcherCount > 0)) { arm.gaps.add(`durable Codex usage missing for ${result?.task_id ?? 'unknown task'}`); arm.codexKnown = false; }
  else if (sawDurable) { arm.gaps.add(`durable Codex usage incomplete for ${result?.task_id ?? 'unknown task'}`); arm.codexKnown = false; }
  return { raw, weighted: mainWeighted, dispatcherCalls: dispatcherCount, codexStartCalls: codexStartCount, sawDispatcherUsage, sawDurable, durableComplete };
}

export async function reportFlow(runDir) {
  const report = { run_dir: runDir, arms: { off: { all_attempts: emptyArm(), final_attempts: emptyArm() }, on: { all_attempts: emptyArm(), final_attempts: emptyArm() } }, coverage_gaps: [] };
  const names = await readdir(runDir);
  const attemptsFile = path.join(runDir, 'attempts.jsonl');
  let attempts = [];
  try { attempts = (await readFile(attemptsFile, 'utf8')).split(/\r?\n/).filter(Boolean).map(JSON.parse); }
  catch (error) { if (error.code !== 'ENOENT') throw error; }
  const finalSessions = new Set(attempts.map(row => row.session_id).filter(Boolean));
  const finalKeys = new Set(attempts.map(row => `${row.arm}\0${row.task_id}\0${row.repetition}`));
  const ledgerBySession = new Map(attempts.filter(row => row.session_id).map(row => [row.session_id, row]));
  const files = names.filter(name => /^(on|off)-.*\.json$/i.test(name)).sort();
  const records = new Map();
  for (const name of files) {
    const record = JSON.parse(await readFile(path.join(runDir, name), 'utf8'));
    const runId = path.basename(name, '.json');
    records.set(runId, record);
  }
  // The ledger identifies the final run for each task/arm; all run records also retain superseded work.
  const matchedSessions = new Set();
  const matchedKeys = new Set();
  for (const [runId, record] of records) {
    const armName = record.arm ?? runId.split('-')[0];
    if (!report.arms[armName]) continue;
    const key = `${armName}\0${record.task_id}\0${record.repetition}`;
    const isFinal = finalSessions.size ? finalSessions.has(record.session_id) : finalKeys.has(key) && record.attempt_status === 'completed';
    if (isFinal) { matchedSessions.add(record.session_id); matchedKeys.add(key); }
    let events = [];
    try { events = (await readFile(path.join(runDir, 'transcripts', runId, 'stdout.jsonl'), 'utf8')).split(/\r?\n/).filter(Boolean).map(JSON.parse); }
    catch (error) { for (const scope of [report.arms[armName].all_attempts, ...(isFinal ? [report.arms[armName].final_attempts] : [])]) { scope.flowKnown = false; scope.dispatchKnown = false; scope.codexKnown = false; scope.gaps.add(`transcript missing or unreadable for ${runId}: ${error.message}`); } }
    const scopes = [report.arms[armName].all_attempts, ...(isFinal ? [report.arms[armName].final_attempts] : [])];
    for (const arm of scopes) {
      arm.attempts++;
      const ledgerRow = ledgerBySession.get(record.session_id) ?? attempts.find(row => row.arm === armName && row.task_id === record.task_id && row.repetition === record.repetition);
      const recordedWorkers = [record.worker_usage, record.usage?.workers, ledgerRow?.worker_usage, ledgerRow?.usage?.workers].find(Array.isArray);
      summarizeAttempt(events, record, arm, recordedWorkers);
    }
  }
  for (const row of attempts) if (row.session_id ? !matchedSessions.has(row.session_id) : !matchedKeys.has(`${row.arm}\0${row.task_id}\0${row.repetition}`)) report.coverage_gaps.push(`final attempt has no run record: ${row.task_id ?? row.session_id ?? 'unknown'}`);
  for (const [name, scopes] of Object.entries(report.arms)) for (const [scopeName, arm] of Object.entries(scopes)) {
    if (!arm.mainKnown) { arm.raw_main_tokens = null; arm.weighted_main_cost = null; }
    if (!arm.dispatchKnown) arm.dispatcher_tokens = null;
    if (!arm.codexKnown) arm.durable_codex_tokens = null;
    if (!arm.flowKnown) { arm.main_requests = null; arm.dispatcher_calls = null; arm.codex_start_calls = null; arm.tools = null; }
    delete arm.mainKnown; delete arm.dispatchKnown; delete arm.codexKnown; delete arm.flowKnown;
    if (arm.tools) arm.tools = Object.fromEntries(Object.entries(arm.tools).sort(([a], [b]) => a.localeCompare(b)));
    arm.coverage_gaps = [...arm.gaps].sort(); delete arm.gaps;
    report.coverage_gaps.push(...arm.coverage_gaps.map(gap => `${name} ${scopeName}: ${gap}`));
    if (arm.attempts === 0) {
      for (const key of ['raw_main_tokens', 'weighted_main_cost', 'dispatcher_calls', 'codex_start_calls', 'dispatcher_tokens', 'durable_codex_tokens', 'main_requests', 'tools']) arm[key] = null;
      report.coverage_gaps.push(`${name} ${scopeName}: no run records found`);
    }
  }
  return report;
}

if (process.argv[1] && path.resolve(process.argv[1]) === path.resolve(fileURLToPath(import.meta.url))) {
  const dir = process.argv[2];
  if (!dir) { console.error('usage: node bench/flow-report.mjs <run-directory>'); process.exitCode = 2; }
  else reportFlow(path.resolve(dir)).then(report => process.stdout.write(`${JSON.stringify(report, null, 2)}\n`)).catch(error => { console.error(error.message); process.exitCode = 1; });
}
