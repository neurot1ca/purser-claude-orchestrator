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

test('draws fixture job records in AbovePrompt and keeps the prior mod drawing', async ($, on) => {
  mock.clock(on)
  const repoRoot = '/repo'
  mock.env(on, { OS: 'Linux', HOME: '/home/test', CLAUDE_CONFIG_DIR: '/custom/claude', OSTYPE: 'linux-gnu', HOSTTYPE: 'x86_64' })
  const logs: string[] = []
  const opened: string[] = []
  const listed: string[] = []
  const now = Date.now() / 1000
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(repoRoot))
  const projectKey = [...new Uint8Array(digest)].map(byte => byte.toString(16).padStart(2, '0')).join('').slice(0, 16)
  const records = [
    { ...files.jobs[0], acceptedAt: now - 60, startedAt: now - 60 },
    files.jobs[1],
    { ...files.jobs[2], finishedAt: now, appliedAt: now },
  ]
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
  }
  on('process.run', () => ({ value: { exitCode: 0, stdout: `${repoRoot}\n`, stderr: '' } }))
  on('session.cwd', () => ({ value: repoRoot }))
  on('session.id', () => ({ value: 'board-fixture' }))
  on('fs.list', (_$, e) => {
    listed.push(e.path)
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
  expect(listed).toHaveLength(2)
  expect(listed.some(path => path.replace(/\\/g, '/').toLowerCase().endsWith('/custom/claude/purser/async_jobs'))).toBe(true)
  const summary = await ui.find({ type: 'Text', text: '⚓' })
  expect(summary?.props.color).toBe('cyan')
  expect(summary?.props.wrap).toBe('truncate-end')
  expect(await ui.find({ type: 'Markdown' })).toBeUndefined()
  expect(await ui.find({ type: 'Button', key: 'purser-board-details' })).toBeDefined()
  expect((await ui.find({ type: 'Text', text: 'Other mod status' }))?.text).toBe('Other mod status')
  await ui.press({ key: 'purser-board-details' })
  expect(opened).toEqual(['purser-job-board'])
  const pane = await $.ui.mount({
    plugin: 'purser', surface: 'terminal', component: 'Pane', requestId: 'purser-job-board',
    props: { title: 'Jobs', isFocused: false, bodyColumns: 80, placement: 'dock', scroll: { offset: 0, bodyRows: 12 }, view: {} },
  })
  const detail = await pane.find({ type: 'Markdown' })
  expect(detail?.text).toContain('| api fix | gpt-6-sol (high) |')
  expect(detail?.text).toContain('| test update | gpt-6-sol (medium) |')
  expect(detail?.text).toContain('| docs | gpt-6-sol (low) |')
  expect((await pane.find({ type: 'Text', text: 'Other mod pane status' }))?.text).toBe('Other mod pane status')
  await pane.unmount()
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
  expect(only?.detail).toContain('| Explore | claude-sonnet | running | 2m 5s | 12 / 34 / 5 / 2 (complete) | Done |')
  expect(only?.detail).toContain('C:\\Users\\雪\\agent.jsonl')
  const mixed = renderJobBoard({ ...files, claude: [claudeRecord] }, 1000)
  expect(mixed?.detail).toContain('| api fix | gpt-6-sol (high) | coding · codex-main active 3s ago |')
  expect(mixed?.detail).toContain('| Explore | claude-sonnet | response_finished |')
  expect(mixed?.summary.map(part => part.text).join('')).not.toContain('Claude subagents')
  expect(mixed?.detail.indexOf('| api fix')).toBeLessThan(mixed?.detail.indexOf('### Claude subagents'))
})

test('sorts running Claude rows first and newest history before limiting to 100 rows', () => {
  const history = Array.from({ length: 101 }, (_, index) => ({ ...claudeRecord,
    id: String(index).padStart(64, '0'), final_excerpt: `history ${index}`,
    observed_at: new Date(Date.parse(claudeRecord.observed_at) + index * 1000).toISOString() }))
  const board = renderJobBoard({ indexes: [], jobs: [], claude: [...history,
    { ...claudeRecord, state: 'running', final_excerpt: 'older running' },
    { ...claudeRecord, state: 'running', observed_at: '2026-11-15T00:00:00Z', final_excerpt: 'newer running' },
  ] })
  expect(board?.detail.split('\n').filter(line => line.startsWith('| Explore |'))).toHaveLength(100)
  const excerpts = board?.detail.split('\n').filter(line => line.startsWith('| Explore |')).map(line => line.split(' | ')[5])
  expect(excerpts).toEqual(['newer running', 'older running', ...history.slice(3).reverse().map(record => record.final_excerpt)])
  expect(board?.detail).toContain('Omitted Claude subagents: 3')
  expect(board?.summary.map(part => part.text).join('')).toContain('Claude subagents running 2')
})

test('hides Claude sessions and escapes excerpt, path, and unknown metadata', () => {
  const board = renderJobBoard({ ...files, claude: [
    { ...claudeRecord, kind: 'session' },
    { ...claudeRecord, id: 'e'.repeat(64), state: 'unknown', duration_ms: null,
      model: '<model>|', usage: { input_tokens: null, output_tokens: 0, completeness: 'partial' },
      final_excerpt: '<script>|*hi*', transcript_path: '/Users/雪/`agent`|.jsonl' },
    { ...claudeRecord, id: 'f'.repeat(64), transcript_path: `\\\\server\\share\\${'x'.repeat(260)}.jsonl` },
  ] }, 1000)
  expect(board?.detail).not.toContain('session')
  expect(board?.detail).toContain('unknown | unknown | unknown / 0 / unknown / unknown (partial)')
  expect(board?.detail).toContain('&lt;script&gt;\\|\\*hi\\*')
  expect(board?.detail).toContain('/Users/雪/')
  expect(board?.detail).toContain('agent')
  expect(board?.detail).toContain(`\\\\server\\share\\${'x'.repeat(260)}`)
})

test('reads only bounded current-project Claude subagents and refreshes both UI surfaces on pane open', async ($, on) => {
  mock.clock(on)
  let repoRoot = '/repo'
  mock.env(on, { OS: 'Linux', HOME: '/home/test', OSTYPE: 'linux-gnu', HOSTTYPE: 'x86_64' })
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(repoRoot))
  const key = [...new Uint8Array(digest)].map(byte => byte.toString(16).padStart(2, '0')).join('').slice(0, 16)
  const good = { ...claudeRecord, state: 'running', project_key: key, transcript_path: '/Users/雪/agent.jsonl' }
  const filesByName = [
    [`${key}-${good.id}.json`, JSON.stringify(good)],
    [`${key}-${'e'.repeat(64)}.json`, JSON.stringify({ ...good, id: 'e'.repeat(64), kind: 'session' })],
    [`${key}-${'f'.repeat(64)}.json`, JSON.stringify({ ...good, id: 'f'.repeat(64), project_key: 'ffffffffffffffff' })],
    [`${key}-${'a'.repeat(64)}.json`, '{bad json'],
    [`${key}-${'b'.repeat(64)}.json`, 'x'.repeat(8193)],
  ]
  const content = Object.fromEntries(filesByName.map(([name, raw]) => [`claude_work/${name}`, raw]))
  on('process.run', () => ({ value: { exitCode: 0, stdout: `${repoRoot}\n`, stderr: '' } }))
  on('session.cwd', () => ({ value: repoRoot }))
  on('fs.list', (_$, e) => e.path.replace(/\\/g, '/').endsWith('/claude_work')
    ? { value: filesByName.map(([name, raw]) => ({ name, kind: 'file', size: raw.length, mtimeMs: 0, isLink: false })) }
    : { deny: 'ENOENT: no such file or directory' })
  on('fs.read', (_$, e) => {
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
    expect(detail).toContain('running')
    expect(detail).toContain('/Users/雪/agent.jsonl')
    expect(detail).not.toContain('session')
    expect(detail).not.toContain('ffffffffffffffff')
    await pane.unmount()
    await ui.unmount()
  }
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
