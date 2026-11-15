import { expect, mock, test } from 'claude-code/testing'
import { renderJobBoard } from './board.js'

const runningId = 'a'.repeat(64)
const queuedId = 'b'.repeat(64)
const doneId = 'c'.repeat(64)
const files = {
  indexes: [
    { jobId: runningId, call: 'run', call_key: 'run-bundle' },
    { jobId: queuedId, call: 'queue' },
    { jobId: doneId, call: 'done' },
  ],
  jobs: [
    { jobId: runningId, call: 'run', state: 'RUNNING', label: 'api fix', model: 'gpt-6-sol', effort: 'high', route: 'normal', acceptedAt: 100, startedAt: 880, activeAgent: 'codex-main', liveness: { lastActivityAt: 997 }, threadId: 'thread-run', phase: 'implementation' },
    { jobId: queuedId, call: 'queue', state: 'QUEUED', label: 'test update', model: 'gpt-6-sol', effort: 'medium', acceptedAt: 970, threadId: 'thread-queue', phase: 'implementation' },
    { jobId: doneId, call: 'done', state: 'SUCCEEDED', label: 'docs', model: 'gpt-6-sol', effort: 'low', finishedAt: 990, appliedAt: 995, applyState: 'applied', boardDisplayCount: 0, threadId: 'thread-done', phase: 'implementation' },
  ],
  leases: { owners: {
    running: { state: 'ACTIVE', job_id: runningId, scope: ['src'] },
    queued: { state: 'QUEUED', job_id: queuedId, scope: ['src'] },
  } },
  bundles: { 'run-bundle': { reviewer1_result: 'PASS' } },
}

test('summarizes mixed job states and keeps the current per-job table for details', () => {
  const board = renderJobBoard(files, 1000)
  expect(board?.summary.map(part => part.text).join('')).toBe('⚓ ● 1 coding · ◌ 1 queued · ✓ 1 done · next 3m')
  expect(board?.summary.filter(part => part.text === ' · ').every(part => part.dimColor)).toBe(true)
  expect(board?.detail).toBe([
    '| ⚓ Task | Model | State | ETA | Review | Apply |',
    '| --- | --- | --- | --- | --- | --- |',
    '| api fix | gpt-6-sol (high) | coding · codex-main active 3s ago | 2m elapsed, 3m left | reviewed | n/a |',
    '| test update | gpt-6-sol (medium) | queued behind api fix | waited 30s | — | — |',
    '| docs | gpt-6-sol (low) | done | — | — | applied |',
  ].join('\n'))
})

test('shows only elapsed time once a job runs past its ETA, with no overdue text', () => {
  const job = { ...files.jobs[0], startedAt: 100, etaMinutes: 5, activeAgent: undefined, liveness: undefined }
  const board = renderJobBoard({ indexes: [files.indexes[0]], jobs: [job] }, 1000)
  expect(board?.detail).toContain('| api fix | gpt-6-sol (high) | coding | 15m elapsed |')
  expect(board?.summary.map(part => part.text).join('')).not.toContain('overdue')
})

test('a succeeded job moves through fetching result, review and acceptance before done', () => {
  const coded = { ...files.jobs[2], applyState: undefined, appliedAt: undefined }
  const index = { ...files.indexes[2], call_key: 'done-bundle' }
  const stage = (job: Record<string, unknown>, bundles: Record<string, Record<string, unknown>>) => {
    const board = renderJobBoard({ indexes: [index], jobs: [job], bundles }, 1000)
    return [board?.summary.map(part => part.text).join(''), board?.detail.split('\n')[2]]
  }
  expect(stage(coded, {})).toEqual(['⚓ ◐ 1 fetching result', '| docs | gpt-6-sol (low) | coded · fetching result | — | — | — |'])
  const bundle = { 'done-bundle': { reviewer1_result: 'PASS', apply: 'codex_apply' } }
  expect(stage(coded, bundle)[0]).toBe('⚓ ◑ 1 in review')
  expect(stage({ ...coded, acceptance: { revision: 'r' } }, bundle)[0]).toBe('⚓ ◕ 1 accepted')
  expect(stage(files.jobs[2], bundle)[0]).toBe('⚓ ✓ 1 done')
  expect(stage({ ...coded, applyState: 'rejected' }, bundle)[0]).toBe('⚓ ✓ 1 done')
  expect(stage({ ...coded, applyState: 'superseded' }, bundle)[0]).toBe('⚓ ✓ 1 done')
})

test('a coded job still moving to review sits above queued jobs', () => {
  const coded = { ...files.jobs[2], applyState: undefined, appliedAt: undefined }
  const board = renderJobBoard({ indexes: files.indexes, jobs: [files.jobs[0], files.jobs[1], coded] }, 1000)
  expect(board?.detail.split('\n').slice(2).map(line => line.split(' | ')[0])).toEqual(['| api fix', '| docs', '| test update'])
})

test('deduplicates running and awaiting-review attempts by thread before counting rows', () => {
  for (const state of ['RUNNING', 'SUCCEEDED']) {
    const prior = { ...files.jobs[0], state, label: 'prior attempt', taskId: 'prior-task', acceptedAt: 100,
      finishedAt: 900, boardDisplayCount: 0 }
    const retry = { ...prior, jobId: queuedId, call: 'retry', state: 'RUNNING', label: 'retry', taskId: 'retry-task', acceptedAt: 200 }
    const board = renderJobBoard({ jobs: [prior, retry], indexes: [files.indexes[0], { jobId: queuedId, call: 'retry' }],
      bundles: { 'run-bundle': { apply: 'codex_apply', reviewer1_result: 'PASS' } } }, 1000)!
    expect(board.detail).not.toContain('prior attempt')
    expect(board.detail).toContain('| retry |')
    expect(board.detail.split('\n')).toHaveLength(3)
    expect(board.summary.map(part => part.text).join('')).toBe('⚓ ● 1 coding · next 3m')
  }
})

test('shows a dim quiet hint from an old running-job activity timestamp', () => {
  const job = { ...files.jobs[0], startedAt: 900, acceptedAt: 900, activeAgent: undefined, liveness: { lastActivityAt: 100 } }
  const board = renderJobBoard({ indexes: [files.indexes[0]], jobs: [job] }, 1000)
  expect(board?.summary.some(part => part.text === 'quiet 15m' && part.dimColor)).toBe(true)
})

test('omits activity text when the job has no activity or checkpoint timestamp', () => {
  const job = { ...files.jobs[0], activeAgent: undefined, liveness: undefined }
  const board = renderJobBoard({ indexes: [files.indexes[0]], jobs: [job] }, 1000)
  expect(board?.detail).toContain('| coding |')
  expect(board?.detail).not.toContain('active')
})

test('draws cached Codex and real-shaped Claude rows in the pane and keeps the prior mod drawing', async ($, on) => {
  mock.clock(on)
  const repoRoot = '/repo'
  mock.env(on, { OS: 'Linux', HOME: '/home/test', CLAUDE_CONFIG_DIR: '/custom/claude', OSTYPE: 'linux-gnu', HOSTTYPE: 'x86_64' })
  const logs: string[] = []
  const opened: string[] = []
  const listed: string[] = []
  let paneCwdUnavailable = false
  let paneReadFails = false
  const now = Date.now() / 1000
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(repoRoot))
  const projectKey = [...new Uint8Array(digest)].map(byte => byte.toString(16).padStart(2, '0')).join('').slice(0, 16)
  const records = [
    { ...files.jobs[0], acceptedAt: now - 60, startedAt: now - 60 },
    files.jobs[1],
    { ...files.jobs[2], finishedAt: now, appliedAt: now },
  ]
  const claudeId = 'e'.repeat(64)
  const realClaudeRecord = { schema_version: 1, id: claudeId, project_key: projectKey, provider: 'claude', kind: 'subagent',
    agent_type: 'Explore', model: 'claude-sonnet', transcript_path: '/home/test/agent-e.jsonl',
    state: 'running', started_at: new Date(now * 1000 - 60_000).toISOString(), observed_at: new Date(now * 1000).toISOString(), metadata_status: 'complete' }
  const indexes = files.indexes.map(index => ({ ...index, project_key: projectKey }))
  const values: Record<string, string> = {
    ...Object.fromEntries(indexes.map(index => [
      `async_jobs/${index.jobId}.json`, JSON.stringify(index),
    ])),
    ...Object.fromEntries(records.map(job => [
      `codex_exec_mcp/jobs/${job.jobId}.json`, JSON.stringify(job),
    ])),
    [`projects/${projectKey}/scope_leases.json`]: JSON.stringify(files.leases),
    ['bundle_artifacts/run-bundle.json']: JSON.stringify(files.bundles['run-bundle']),
    [`claude_work/${projectKey}-${claudeId}.json`]: JSON.stringify(realClaudeRecord),
  }
  on('process.run', () => ({ value: { exitCode: 0, stdout: `${repoRoot}\n`, stderr: '' } }))
  on('session.cwd', () => paneCwdUnavailable ? { deny: 'cwd unavailable in pane' } : { value: repoRoot })
  on('session.id', () => ({ value: 'board-fixture' }))
  on('fs.list', (_$, e) => {
    listed.push(e.path)
    if (paneReadFails) return { deny: 'fixture pane refresh failure' }
    if (e.path.replace(/\\/g, '/').endsWith('/claude_work')) return ({
      value: [{ name: `${projectKey}-${claudeId}.json`, kind: 'file', size: JSON.stringify(realClaudeRecord).length, mtimeMs: now, isLink: false }],
    })
    return ({
    value: indexes.map(index => ({ name: `${index.jobId}.json`, kind: 'file', size: 1, mtimeMs: now, isLink: false })),
    })
  })
  on('fs.read', (_$, e) => {
    const parts = e.path.split(/[\\/]/)
    const rootIndex = parts.lastIndexOf('purser')
    const relative = parts.slice(rootIndex + 1).join('/')
    return values[relative] === undefined ? { deny: 'unexpected fixture path' } : { value: values[relative] }
  })
  on('ui.log', (_$, e) => { logs.push(e.text); return { value: undefined } })
  on('ui.open', (_$, e) => { opened.push(e.id); return { value: { isPlaced: true } } })
  on('ui.render', { component: 'AbovePrompt' }, ($, e) => {
    const { Text } = $.ui.resolve(e)
    return Text({ children: 'Other mod status' })
  })
  on('ui.render', { component: 'Pane' }, ($, e) => {
    const { Text } = $.ui.resolve(e)
    return Text({ children: 'Other mod pane status' })
  })
  const ui = await $.ui.mount({
    plugin: 'purser', surface: 'terminal', component: 'AbovePrompt',
    props: { hasSurvey: false, isWorking: false, maxRows: 12, bodyColumns: 100, scroll: { offset: 0, bodyRows: 12 }, view: {} },
  })
  expect(logs).toEqual([])
  expect(listed).toHaveLength(4)
  expect(listed.some(path => path.replace(/\\/g, '/').toLowerCase().endsWith('/custom/claude/purser/async_jobs'))).toBe(true)
  expect(listed.some(path => path.replace(/\\/g, '/').toLowerCase().endsWith('/.orchestrator'))).toBe(true)
  expect(listed.some(path => path.replace(/\\/g, '/').toLowerCase().endsWith('/bundle_artifacts'))).toBe(true)
  const summary = await ui.find({ type: 'Text', text: '⚓' })
  expect(summary?.props.color).toBe('cyan')
  expect(summary?.props.wrap).toBe('truncate-end')
  expect(await ui.find({ type: 'Markdown' })).toBeUndefined()
  expect(await ui.find({ type: 'Button', key: 'purser-board-details' })).toBeDefined()
  expect((await ui.find({ type: 'Text', text: 'Other mod status' }))?.text).toBe('Other mod status')
  await ui.press({ key: 'purser-board-details' })
  expect(opened).toEqual(['purser-job-board'])
  paneCwdUnavailable = true
  const pane = await $.ui.mount({
    plugin: 'purser', surface: 'terminal', component: 'Pane', requestId: 'purser-job-board',
    props: { title: 'Jobs', isFocused: false, bodyColumns: 80, placement: 'dock', scroll: { offset: 0, bodyRows: 12 }, view: {} },
  })
  const detail = await pane.find({ type: 'Markdown' })
  expect(detail?.text).toContain('| api fix | gpt-6-sol (high) |')
  expect(detail?.text).toContain('| test update | gpt-6-sol (medium) |')
  expect(detail?.text).toContain('| docs | gpt-6-sol (low) |')
  expect(detail?.text).toContain('### Claude subagents')
  expect(detail?.text).toContain('| Explore | claude-sonnet | running |')
  expect((await pane.find({ type: 'Text', text: 'Other mod pane status' }))?.text).toBe('Other mod pane status')
  await pane.unmount()
  paneCwdUnavailable = false
  paneReadFails = true
  const failedRefreshPane = await $.ui.mount({
    plugin: 'purser', surface: 'terminal', component: 'Pane', requestId: 'purser-job-board',
    props: { title: 'Jobs', isFocused: false, bodyColumns: 80, placement: 'dock', scroll: { offset: 0, bodyRows: 12 }, view: {} },
  })
  expect((await failedRefreshPane.find({ type: 'Markdown' }))?.text).toContain('| api fix | gpt-6-sol (high) |')
  expect((await failedRefreshPane.find({ type: 'Markdown' }))?.text).toContain('| docs | gpt-6-sol (low) |')
  expect((await failedRefreshPane.find({ type: 'Markdown' }))?.text).toContain('| Explore | claude-sonnet | running |')
  await failedRefreshPane.unmount()
  await ui.unmount()
})

test('returns no board when this project has no jobs', () => {
  expect(renderJobBoard({ indexes: [], jobs: [] })).toBeUndefined()
})

const claudeRecord = {
  schema_version: 1, id: 'd'.repeat(64), project_key: '0123456789abcdef', provider: 'claude', kind: 'subagent',
  agent_type: 'Explore', model: 'claude-sonnet', state: 'response_finished', duration_ms: 125000,
  usage: { input_tokens: 12, output_tokens: 34, cache_read_input_tokens: 5, cache_creation_input_tokens: 2, completeness: 'complete' },
  final_excerpt: 'Done', transcript_path: 'C:\\Users\\雪\\agent.jsonl', observed_at: '2026-11-14T10:00:00Z', metadata_status: 'complete',
}

test('renders Claude-only and mixed provider details without changing Codex rows', () => {
  expect(renderJobBoard({ indexes: [], jobs: [], claude: [claudeRecord] }, 1000)).toBeUndefined()
  expect(renderJobBoard({ indexes: [], jobs: [], claude: [{ ...claudeRecord, state: 'unknown' }] }, 1000)).toBeUndefined()
  const only = renderJobBoard({ indexes: [], jobs: [], claude: [{ ...claudeRecord, state: 'running' }] }, 1000)
  expect(only?.summary.map(part => part.text).join('')).toContain('Claude subagents running 1')
  expect(only?.detail).toContain('| Explore | claude-sonnet | running | 2m 5s |')
  expect(only?.detail).not.toContain('Transcript path')
  expect(only?.detail).not.toContain('C:\\Users\\雪\\agent.jsonl')
  const mixed = renderJobBoard({ ...files, claude: [claudeRecord] }, 1000)
  expect(mixed?.detail).toContain('| api fix | gpt-6-sol (high) | coding · codex-main active 3s ago |')
  expect(mixed?.detail).toContain('| Explore | claude-sonnet | done |')
  expect(mixed?.summary.map(part => part.text).join('')).not.toContain('Claude subagents')
  expect(mixed?.detail.indexOf('| api fix')).toBeLessThan(mixed?.detail.indexOf('### Claude subagents'))
})

test('adds an escaped task label only to dispatchers and keeps the compact columns', () => {
  const board = renderJobBoard({ indexes: [], jobs: [], claude: [
    { ...claudeRecord, state: 'running', agent_type: 'purser:dispatcher', task_label: 'Fix <api>|*' },
    { ...claudeRecord, agent_type: 'purser:dispatcher' },
    { ...claudeRecord, task_label: 'not a dispatcher' },
  ] }, 1000)!
  expect(board.detail).toContain('| purser:dispatcher · Fix &lt;api&gt;\\|\\* |')
  expect(board.detail).toContain('| purser:dispatcher |')
  expect(board.detail).not.toContain('not a dispatcher')
  expect(board.detail).not.toContain('Done')
  expect(board.detail).not.toContain('12 / 34')
  expect(board.detail.split('\n').filter(line => line.startsWith('| purser:dispatcher')).every(line => line.split(' | ').length === 4)).toBe(true)
})

test('normalizes Claude terminal states and hides rows without identifying metadata', () => {
  const now = Date.parse(claudeRecord.observed_at) / 1000
  const board = renderJobBoard({ indexes: [], jobs: [], claude: [
    { ...claudeRecord, state: 'response_finished' },
    { ...claudeRecord, id: 'a'.repeat(64), state: 'finished' },
    { ...claudeRecord, id: 'b'.repeat(64), state: 'session_ended' },
    { ...claudeRecord, id: 'c'.repeat(64), state: 'custom_state' },
    { ...claudeRecord, id: 'd'.repeat(64), state: 'running' },
    { ...claudeRecord, id: 'e'.repeat(64), agent_type: null, model: '', transcript_path: '', usage: null },
    { ...claudeRecord, id: 'f'.repeat(64), agent_type: null, model: '', transcript_path: '', usage: { input_tokens: null, output_tokens: null, cache_read_input_tokens: null, cache_creation_input_tokens: null } },
    { ...claudeRecord, id: '0'.repeat(64), agent_type: null, model: '', transcript_path: '/missing/subagents/agent-phantom.jsonl', usage: null },
  ] }, now)
  expect(board?.detail).toContain('| Explore | claude-sonnet | done |')
  expect(board?.detail).toContain('| Explore | claude-sonnet | ended |')
  expect(board?.detail).toContain('| Explore | claude-sonnet | custom_state |')
  expect(board?.detail).not.toContain('response_finished')
  expect(board?.detail).not.toContain('session_ended')
  expect(board?.detail).not.toContain('/missing/subagents/agent-phantom.jsonl')
  expect(board?.detail.split('\n').filter(line => line.includes('claude-sonnet'))).toHaveLength(5)
})

test('sorts running Claude rows first and newest history before limiting to 100 rows', () => {
  const now = Date.parse(claudeRecord.observed_at) / 1000 + 120
  const history = Array.from({ length: 101 }, (_, index) => ({ ...claudeRecord,
    id: String(index).padStart(64, '0'), model: `history ${index}`,
    observed_at: new Date(Date.parse(claudeRecord.observed_at) + index * 1000).toISOString() }))
  const board = renderJobBoard({ indexes: [], jobs: [], claude: [...history,
    { ...claudeRecord, state: 'running', model: 'older running' },
    { ...claudeRecord, state: 'running', observed_at: '2026-11-15T00:00:00Z', model: 'newer running' },
  ] }, now)
  expect(board?.detail.split('\n').filter(line => line.startsWith('| Explore |'))).toHaveLength(100)
  const models = board?.detail.split('\n').filter(line => line.startsWith('| Explore |')).map(line => line.split(' | ')[1])
  expect(models).toEqual(['newer running', 'older running', ...history.slice(3).reverse().map(record => record.model)])
  expect(board?.detail).toContain('Omitted Claude subagents: 3')
  expect(board?.summary.map(part => part.text).join('')).toContain('Claude subagents running 2')
})

test('keeps running and recent Claude subagents and drops stale finished rows', () => {
  const now = Date.parse('2026-11-15T12:00:00Z') / 1000
  const board = renderJobBoard({ indexes: [], jobs: [], claude: [
    { ...claudeRecord, id: 'a'.repeat(64), model: 'running', state: 'running', observed_at: undefined },
    { ...claudeRecord, id: 'b'.repeat(64), model: 'recent', observed_at: new Date((now - 600) * 1000).toISOString() },
    { ...claudeRecord, id: 'c'.repeat(64), model: 'stale', observed_at: new Date((now - 7200) * 1000).toISOString() },
  ] }, now)!
  expect(board.detail).toContain('| Explore | running | running |')
  expect(board.detail).toContain('| Explore | recent | done |')
  expect(board.detail).not.toContain('stale')
  expect(board.detail).not.toContain('Omitted Claude subagents:')
  expect(board.summary.map(part => part.text).join('')).toContain('Claude subagents running 1')
})

test('hides Claude sessions, tokens and excerpts, and escapes paths and metadata', () => {
  const board = renderJobBoard({ ...files, claude: [
    { ...claudeRecord, kind: 'session' },
    { ...claudeRecord, id: 'e'.repeat(64), state: 'unknown', duration_ms: null,
      model: '<model>|', usage: { input_tokens: null, output_tokens: 0, completeness: 'partial' },
      final_excerpt: '<script>|*hi*', transcript_path: '/Users/雪/`agent`|.jsonl' },
    { ...claudeRecord, id: 'f'.repeat(64), transcript_path: `\\\\server\\share\\${'x'.repeat(260)}.jsonl` },
  ] }, 1000)
  expect(board?.detail).not.toContain('session')
  expect(board?.detail).toContain('&lt;model&gt;\\| | unknown | unknown |')
  expect(board?.detail).not.toContain('&lt;script&gt;')
  expect(board?.detail).not.toContain('Excerpt')
  expect(board?.detail).not.toContain('Tokens')
  expect(board?.detail).not.toContain('Transcript path')
  expect(board?.detail).not.toContain('/Users/雪/`agent`|.jsonl')
  expect(board?.detail).not.toContain(`\\\\server\\share\\${'x'.repeat(260)}`)
})

test('reads only bounded current-project Claude subagents and refreshes both UI surfaces on pane open', async ($, on) => {
  mock.clock(on)
  let repoRoot = '/repo'
  let runtimeOS = 'Linux'
  on('env.get', (_$, e) => ({ value: e.name === 'OS' ? runtimeOS : e.name === 'HOME' ? '/home/test' :
    e.name === 'USERPROFILE' ? 'C:\\Users\\test' : e.name === 'OSTYPE' ? 'linux-gnu' : e.name === 'HOSTTYPE' ? 'x86_64' : undefined }))
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(repoRoot))
  const key = [...new Uint8Array(digest)].map(byte => byte.toString(16).padStart(2, '0')).join('').slice(0, 16)
  const parentTranscript = '/home/test/.claude/projects/p/session-1.jsonl'
  const agentTranscript = '/home/test/.claude/projects/p/session-1/subagents/agent-agent-1.jsonl'
  const good = { ...claudeRecord, state: 'running', project_key: key, agent_id: 'agent-1', session_id: 'session-1',
    model: '', model_source: '', transcript_path: parentTranscript }
  const filesByName = [
    [`${key}-${good.id}.json`, JSON.stringify(good)],
    [`${key}-${'e'.repeat(64)}.json`, JSON.stringify({ ...good, id: 'e'.repeat(64), kind: 'session' })],
    [`${key}-${'f'.repeat(64)}.json`, JSON.stringify({ ...good, id: 'f'.repeat(64), project_key: 'ffffffffffffffff' })],
    [`${key}-${'a'.repeat(64)}.json`, '{bad json'],
    [`${key}-${'b'.repeat(64)}.json`, 'x'.repeat(8193)],
  ]
  const content = Object.fromEntries(filesByName.map(([name, raw]) => [`claude_work/${name}`, raw]))
  let transcriptReads = 0
  let transcriptSize = 8192
  let transcriptLocked = false
  on('process.run', () => ({ value: { exitCode: 0, stdout: `${repoRoot}\n`, stderr: '' } }))
  on('session.cwd', () => ({ value: repoRoot }))
  on('fs.list', (_$, e) => {
    const path = e.path.replace(/\\/g, '/')
    if (path.endsWith('/claude_work')) return { value: filesByName.map(([name, raw]) => ({ name, kind: 'file', size: raw.length, mtimeMs: 0, isLink: false })) }
    if (path.endsWith('/session-1/subagents')) return { value: [{ name: 'agent-agent-1.jsonl', kind: 'file', size: transcriptSize, mtimeMs: 0, isLink: false }] }
    return { deny: 'ENOENT: no such file or directory' }
  })
  on('fs.read', (_$, e) => {
    if (e.path.replace(/\\/g, '/').endsWith('/subagents/agent-agent-1.jsonl')) {
      transcriptReads++
      if (transcriptLocked) return { deny: 'EACCES: antivirus lock' }
      return { value: [
        JSON.stringify({ type: 'user', message: { role: 'user', content: 'x'.repeat(5000) } }),
        JSON.stringify({ type: 'assistant', sessionId: 'session-1', agentId: 'agent-1', message: { role: 'assistant', model: 'claude-haiku-5-5' } }),
        JSON.stringify({ type: 'assistant', message: { role: 'assistant', model: 'later-model' } }),
      ].join('\n') }
    }
    const name = e.path.split(/[\\/]/).pop()!
    const raw = content[`claude_work/${name}`]
    return raw === undefined ? { deny: 'unexpected fixture path' } : { value: raw }
  })
  on('ui.log', () => ({ value: undefined }))
  on('ui.open', () => ({ value: { isPlaced: true } }))
  on('ui.render', { component: 'AbovePrompt' }, ($, e) => {
    const { Text } = $.ui.resolve(e)
    return Text({ children: 'Other mod status' })
  })
  on('ui.render', { component: 'Pane' }, ($, e) => {
    const { Text } = $.ui.resolve(e)
    return Text({ children: 'Other mod pane status' })
  })
  for (const surface of ['terminal', 'desktop']) {
    const ui = await $.ui.mount({ plugin: 'purser', surface, component: 'AbovePrompt',
      props: { hasSurvey: false, isWorking: false, maxRows: 8, bodyColumns: 80, scroll: { offset: 0, bodyRows: 8 }, view: {} } })
    expect((await ui.find({ type: 'Text', text: /Claude subagents running 1/ }))?.text).toContain('Claude subagents running 1')
    const pane = await $.ui.mount({ plugin: 'purser', surface, component: 'Pane', requestId: 'purser-job-board',
      props: { title: 'Jobs', isFocused: false, bodyColumns: 80, placement: 'dock', scroll: { offset: 0, bodyRows: 12 }, view: {} } })
    const detail = (await pane.find({ type: 'Markdown' }))?.text
    expect(detail).toContain('Claude subagents')
    expect(detail).toContain('| Explore | claude-haiku-5-5 | running |')
    expect(detail).toContain('running')
    expect(detail).not.toContain('Transcript path')
    expect(detail).not.toContain(agentTranscript)
    expect(detail).not.toContain('session')
    expect(detail).not.toContain('ffffffffffffffff')
    await pane.unmount()
    await ui.unmount()
  }
  expect(transcriptReads).toBeGreaterThan(0)
  for (const scenario of [
    { model: 'claude-opus-5-5', model_source: 'agent_meta', size: 8192, locked: false, path: parentTranscript, want: 'claude-haiku-5-5', reads: 1 },
    { model: 'stored-transcript-model', model_source: 'transcript', size: 8192, locked: false, path: parentTranscript, want: 'stored-transcript-model', reads: 0 },
    { model: 'fallback', model_source: 'agent_meta', size: 65537, locked: false, path: parentTranscript, want: 'fallback', reads: 0 },
    { model: 'fallback', model_source: 'agent_meta', size: 8192, locked: true, path: parentTranscript, want: 'fallback', reads: 1 },
    { model: 'fallback', model_source: 'agent_meta', size: 8192, locked: false, path: parentTranscript.replace('session-1.jsonl', 'other.jsonl'), want: 'fallback', reads: 0 },
    { model: 'fallback', model_source: 'agent_meta', size: 8192, locked: false, path: parentTranscript.replace('/p/', '/p/../'), want: 'fallback', reads: 0 },
    { model: 'fallback', model_source: 'agent_meta', size: 8192, locked: false, path: parentTranscript.replace('session-1.jsonl', 'SESSION-1.JSONL'), want: 'fallback', reads: 0 },
    { model: 'fallback', model_source: 'agent_meta', size: 8192, locked: false, path: 'C:\\Users\\test\\.claude\\projects\\p\\SESSION-1.JSONL', want: 'claude-haiku-5-5', reads: 1 },
  ]) {
    runtimeOS = scenario.path.startsWith('C:') ? 'Windows_NT' : 'Linux'
    content[`claude_work/${filesByName[0][0]}`] = JSON.stringify({ ...good, model: scenario.model, model_source: scenario.model_source, transcript_path: scenario.path })
    transcriptSize = scenario.size
    transcriptLocked = scenario.locked
    const before = transcriptReads
    const pane = await $.ui.mount({ plugin: 'purser', surface: 'terminal', component: 'Pane', requestId: 'purser-job-board',
      props: { title: 'Jobs', isFocused: false, bodyColumns: 80, placement: 'dock', scroll: { offset: 0, bodyRows: 12 }, view: {} } })
    expect((await pane.find({ type: 'Markdown' }))?.text).toContain(`| Explore | ${scenario.want} | running |`)
    expect(transcriptReads - before).toBe(scenario.reads)
    await pane.unmount()
  }
  runtimeOS = 'Linux'
  repoRoot = '/other-project'
  const other = await $.ui.mount({ plugin: 'purser', surface: 'terminal', component: 'AbovePrompt',
    props: { hasSurvey: false, isWorking: false, maxRows: 8, bodyColumns: 80, scroll: { offset: 0, bodyRows: 8 }, view: {} } })
  expect(await other.find({ type: 'Text', text: /Claude subagents/ })).toBeUndefined()
  const otherPane = await $.ui.mount({ plugin: 'purser', surface: 'terminal', component: 'Pane', requestId: 'purser-job-board',
    props: { title: 'Jobs', isFocused: false, bodyColumns: 80, placement: 'dock', scroll: { offset: 0, bodyRows: 12 }, view: {} } })
  expect(await otherPane.find({ type: 'Markdown' })).toBeUndefined()
  await otherPane.unmount()
  await other.unmount()
})

test('ignores malformed or mismatched job records', () => {
  expect(renderJobBoard({
    indexes: [{ jobId: runningId, call: 'expected' }],
    jobs: [{ jobId: runningId, call: 'other', state: 'RUNNING', label: 'wrong project job' }],
  })).toBeUndefined()
})

test('Claude listing and read failures leave Codex rows refreshing', async ($, on) => {
  mock.clock(on)
  on('ui.render', ($, e) => {
    const { Text } = $.ui.resolve(e)
    return Text({ children: 'Other mod status' })
  })
  mock.env(on, { OS: 'Linux', HOME: '/home/test' })
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode('/repo'))
  const key = [...new Uint8Array(digest)].map(byte => byte.toString(16).padStart(2, '0')).join('').slice(0, 16)
  let failure = 'EACCES'
  let label = ''
  on('session.cwd', () => ({ value: '/repo' }))
  on('process.run', () => ({ value: { exitCode: 0, stdout: '/repo\n', stderr: '' } }))
  on('fs.list', (_$, e) => {
    if (e.path.replace(/\\/g, '/').endsWith('/claude_work')) return failure === 'read'
      ? { value: [{ name: `${key}-${claudeRecord.id}.json`, kind: 'file', size: 100, isLink: false }] }
      : { deny: failure }
    return { value: [{ name: `${runningId}.json`, kind: 'file' }] }
  })
  on('fs.read', (_$, e) => {
    const path = e.path.replace(/\\/g, '/')
    if (path.includes('/claude_work/')) return { deny: 'EBUSY' }
    if (path.includes('/async_jobs/')) return { value: JSON.stringify({ ...files.indexes[0], project_key: key }) }
    if (path.includes('/codex_exec_mcp/jobs/')) return { value: JSON.stringify({ ...files.jobs[0], label }) }
    return { deny: 'ENOENT' }
  })
  const logs: string[] = []
  on('ui.log', (_$, e) => { logs.push(e.text); return { value: undefined } })
  for (const mode of ['EACCES', 'EBUSY', 'read']) {
    failure = mode
    label = `Codex refresh ${mode}`
    const ui = await $.ui.mount({ plugin: 'purser', surface: 'terminal', component: 'AbovePrompt',
      props: { hasSurvey: false, isWorking: false, maxRows: 8, bodyColumns: 80, scroll: { offset: 0, bodyRows: 8 }, view: {} } })
    expect(await ui.find({ type: 'Button', key: 'purser-board-details' })).toBeDefined()
    const pane = await $.ui.mount({ plugin: 'purser', surface: 'terminal', component: 'Pane', requestId: 'purser-job-board',
      props: { title: 'Jobs', isFocused: false, bodyColumns: 80, placement: 'dock', scroll: { offset: 0, bodyRows: 12 }, view: {} } })
    expect((await pane.find({ type: 'Markdown' }))?.text).toContain(label)
    await pane.unmount()
    await ui.unmount()
  }
  expect(logs).toEqual([])
})

test('leaves another mod drawing intact when Purser has no board', async ($, on) => {
  mock.clock(on)
  on('ui.log', () => ({ value: undefined }))
  on('ui.render', { component: 'AbovePrompt' }, ($, e) => {
    const { Text } = $.ui.resolve(e)
    return Text({ children: 'Other mod status' })
  })
  const ui = await $.ui.mount({
    plugin: 'purser', surface: 'terminal', component: 'AbovePrompt',
    props: { hasSurvey: false, isWorking: false, maxRows: 8, bodyColumns: 80, scroll: { offset: 0, bodyRows: 8 }, view: {} },
  })
  expect((await ui.find({ type: 'Text', text: 'Other mod status' }))?.text).toBe('Other mod status')
  expect(await ui.find({ type: 'Button', key: 'purser-board-details' })).toBeUndefined()
  expect(await ui.find({ type: 'Markdown' })).toBeUndefined()
  await ui.unmount()
})

test('a missing async jobs directory is empty and does not log a refresh failure', async ($, on) => {
  mock.clock(on)
  const repoRoot = '/repo'
  mock.env(on, { OS: 'Linux', HOME: '/home/test', OSTYPE: 'linux-gnu', HOSTTYPE: 'x86_64' })
  const logs: string[] = []
  on('process.run', () => ({ value: { exitCode: 0, stdout: `${repoRoot}\n`, stderr: '' } }))
  on('session.cwd', () => ({ value: repoRoot }))
  on('session.id', () => ({ value: 'board-empty' }))
  on('fs.list', () => ({ deny: 'ENOENT: no such file or directory' }))
  on('ui.log', (_$, e) => { logs.push(e.text); return { value: undefined } })
  on('ui.render', { component: 'AbovePrompt' }, ($, e) => {
    const { Text } = $.ui.resolve(e)
    return Text({ children: 'Other mod status' })
  })
  const ui = await $.ui.mount({
    plugin: 'purser', surface: 'terminal', component: 'AbovePrompt',
    props: { hasSurvey: false, isWorking: false, maxRows: 8, bodyColumns: 80, scroll: { offset: 0, bodyRows: 8 }, view: {} },
  })
  expect(logs).toEqual([])
  expect(await ui.find({ type: 'Markdown' })).toBeUndefined()
  expect((await ui.find({ type: 'Text', text: 'Other mod status' }))?.text).toBe('Other mod status')
  await ui.unmount()
})

test('a non-git project with no jobs is empty and does not log a refresh failure', async ($, on) => {
  mock.clock(on)
  mock.env(on, { OS: 'Linux', HOME: '/home/test', OSTYPE: 'linux-gnu', HOSTTYPE: 'x86_64' })
  const logs: string[] = []
  on('process.run', () => ({ value: { exitCode: 128, stdout: '', stderr: 'fatal: not a git repository (or any of the parent directories): .git' } }))
  on('session.cwd', () => ({ value: '/non-git-project' }))
  on('session.id', () => ({ value: 'board-non-git' }))
  on('ui.log', (_$, e) => { logs.push(e.text); return { value: undefined } })
  on('ui.render', { component: 'AbovePrompt' }, ($, e) => {
    const { Text } = $.ui.resolve(e)
    return Text({ children: 'Other mod status' })
  })
  const ui = await $.ui.mount({
    plugin: 'purser', surface: 'terminal', component: 'AbovePrompt',
    props: { hasSurvey: false, isWorking: false, maxRows: 8, bodyColumns: 80, scroll: { offset: 0, bodyRows: 8 }, view: {} },
  })
  expect(logs).toEqual([])
  expect(await ui.find({ type: 'Button', key: 'purser-board-details' })).toBeUndefined()
  expect((await ui.find({ type: 'Text', text: 'Other mod status' }))?.text).toBe('Other mod status')
  await ui.unmount()
})

test('one board read failure stays quiet and a later success clears it', async ($, on) => {
  mock.clock(on)
  let failRead = true
  const logs: string[] = []
  on('env.get', (_$, e) => ({
    value: e.name === 'OS' ? 'Linux' : e.name === 'HOME' ? '/home/test' :
      e.name === 'OSTYPE' ? 'linux-gnu' : e.name === 'HOSTTYPE' ? 'x86_64' : undefined,
  }))
  on('process.run', (_$, e) => ({
    value: { exitCode: 0, stdout: e.init.stdin ? '' : '/repo\n', stderr: '' },
  }))
  on('session.cwd', () => ({ value: '/repo' }))
  on('session.id', () => ({ value: 'board-recovery' }))
  on('fs.list', () => failRead ? { deny: 'fixture read failure' } : { value: [] })
  on('ui.log', (_$, e) => { logs.push(e.text); return { value: undefined } })
  on('ui.render', { component: 'AbovePrompt' }, ($, e) => {
    const { Text } = $.ui.resolve(e)
    return Text({ children: 'Other mod status' })
  })
  const ui = await $.ui.mount({
    plugin: 'purser', surface: 'terminal', component: 'AbovePrompt',
    props: { hasSurvey: false, isWorking: false, maxRows: 8, bodyColumns: 80, scroll: { offset: 0, bodyRows: 8 }, view: {} },
  })
  expect(logs).toEqual([])
  await ui.unmount()
  failRead = false
  const recoveredUi = await $.ui.mount({
    plugin: 'purser', surface: 'terminal', component: 'AbovePrompt',
    props: { hasSurvey: false, isWorking: false, maxRows: 8, bodyColumns: 80, scroll: { offset: 0, bodyRows: 8 }, view: {} },
  })
  expect(logs).toEqual([])
  await recoveredUi.unmount()
})

test('board read errors fail silently until repeated and log once per session', async ($, on) => {
  const clock = mock.clock(on)
  const logs: string[] = []
  on('env.get', (_$, e) => ({
    value: e.name === 'OS' ? 'Linux' : e.name === 'HOME' ? '/home/test' :
      e.name === 'OSTYPE' ? 'linux-gnu' : e.name === 'HOSTTYPE' ? 'x86_64' : undefined,
  }))
  on('process.run', (_$, e) => ({
    value: { exitCode: 0, stdout: e.init.stdin ? '' : '/repo\n', stderr: '' },
  }))
  on('session.cwd', () => ({ value: '/repo' }))
  on('session.id', () => ({ value: 'board-error' }))
  on('fs.list', () => ({ deny: 'fixture read failure' }))
  on('ui.log', (_$, e) => { logs.push(e.text); return { value: undefined } })
  on('ui.render', { component: 'AbovePrompt' }, ($, e) => {
    const { Text } = $.ui.resolve(e)
    return Text({ children: 'Other mod status' })
  })
  on('classic.UserPromptSubmit', () => ({ additionalContext: ['existing context'] }))

  const result = await $.classic.UserPromptSubmit({ session_id: 'board-error', cwd: '/repo', prompt: 'Continue' })
  const ui = await $.ui.mount({
    plugin: 'purser', surface: 'terminal', component: 'AbovePrompt',
    props: { hasSurvey: false, isWorking: false, maxRows: 8, bodyColumns: 80, scroll: { offset: 0, bodyRows: 8 }, view: {} },
  })
  expect(result.additionalContext).toEqual(['existing context'])
  expect((await ui.find({ type: 'Text', text: 'Other mod status' }))?.text).toBe('Other mod status')
  expect(await ui.find({ text: /Purser jobs/ })).toBeUndefined()
  expect(logs).toEqual([])
  await clock.advance(10_000)
  expect(logs).toEqual([
    'Purser job board refresh failed; turn continued unchanged. (board read: HooksError: purser: $.fs.list: fixture read failure)',
  ])
  expect(logs).toHaveLength(1)
  await ui.unmount()
})
