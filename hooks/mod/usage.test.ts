import { expect, mock, test } from 'claude-code/testing'
import { barColor, formatUsageNumber, mapSessionUsage, parseSelectedAgents, pillSvg, readCodexModel, readQuotaSnapshot, readSelectedAgents, registerUsage, terminalBar, timeElapsedTick, usageGroups } from './usage.js'
import { registerBoard } from './board.js'

const now = Date.parse('2026-10-04T12:00:00Z')
const quotaRecord = (five = 69, seven = 90) => ({ codex_quota: {
  '5h_used_pct': five, '5h_resets_at': '2026-10-04T14:30:00Z',
  '7d_used_pct': seven, '7d_resets_at': '2026-10-05T19:00:00Z', snapshot_at: '2026-10-04T11:00:00Z',
} })
const usage = {
  startedAt: now - 60_000,
  context: { tokens: 15_600, window: 200_000, percent: 42.5 },
  rateLimits: [
    { kind: 'five_hour', percentUsed: 20, resetsAt: '2026-10-04T14:40:00Z' },
    { kind: 'seven_day', percentUsed: 30, resetsAt: '2026-10-05T19:00:00Z' },
  ],
  cost: { usd: 4.32 },
}

const agentFiles = {
  indexes: [{ jobId: 'a'.repeat(64), call: 'coder', call_key: 'a'.repeat(64) }, { jobId: 'b'.repeat(64), call: 'free' }],
  jobs: [
    { jobId: 'b'.repeat(64), call: 'free', activeAgent: 'opencode', model: 'beam-free', startedAt: now / 1000 - 10 },
    { jobId: 'a'.repeat(64), call: 'coder', servedAgent: 'codex', servedModel: 'gpt-6.1-sol', model: 'original-request', startedAt: now / 1000 - 60 },
  ],
  bundles: { ['a'.repeat(64)]: { reviewer1_result: 'NO_MATERIAL_FINDING_IN_REVIEWED_SCOPE', reviewer1_provider: 'agy', reviewer1_model_requested: 'gemini-3.8-flash-high' } },
}
const selectedConfig = '[roles]\r\ncoder = "codex"\r\nreviewer = "claude"\r\n[coding]\r\ncoder_agent = "codex" # selected coder\r\n[reviewer]\r\nselected = { agent = "agy", model = "gemini-3.8-flash-high" } # selected reviewer\r\n'

test('shows selected agents after Codex and keeps free sourced from board reads', async ($, on) => {
  const groups = usageGroups({ model: 'gpt-6.1-sol' }, mapSessionUsage(usage, now), now, agentFiles, parseSelectedAgents(selectedConfig))
  expect(groups.map(group => group.map(pill => pill.id))).toEqual([
    ['claude-5h', 'claude-7d', 'claude-context'], ['codex-model'], ['coder', 'reviewer', 'free'], ['cost'],
  ])
  expect(groups[2].map(pill => [pill.label, pill.value])).toEqual([
    ['coder', 'codex'], ['reviewer', 'agy · gemini-3.8-flash-high'], ['free', 'opencode · beam-free'],
  ])
  expect(groups[2].slice(0, 2).map(pill => pill.detail)).toEqual(['Coder selected for Purser', 'Reviewer selected for Purser'])
  expect(usageGroups({}, { quota: {} }, now, { ...agentFiles, jobs: [{ ...agentFiles.jobs[0], model: 'opencode/beam-free' }] })[0][0].value).toBe('opencode · beam-free')
  for (const pill of groups[2]) {
    expect(terminalBar(pill)).toBeUndefined()
    expect(pillSvg(pill)).not.toContain('height="4"')
    expect(pillSvg(pill)).toContain(`<title>${pill.detail}</title>`)
  }
  mock.clock(on)
  mock.env(on, { OS: 'Linux', HOME: '/home/test' })
  on('session.cwd', () => ({ value: '/agent-project' }))
  on('session.usage', () => ({ value: {} }))
  on('process.run', () => ({ value: { exitCode: 0, stdout: '/agent-project\n', stderr: '' } }))
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode('/agent-project'))
  const key = [...new Uint8Array(digest)].map(byte => byte.toString(16).padStart(2, '0')).join('').slice(0, 16)
  const records = Object.fromEntries([
    ...agentFiles.indexes.map(index => [`async_jobs/${index.jobId}.json`, { ...index, project_key: key }]),
    ...agentFiles.jobs.map(job => [`codex_exec_mcp/jobs/${job.jobId}.json`, { ...job, startedAt: Date.now() / 1000 - 60 }]),
    [`bundle_artifacts/${'a'.repeat(64)}.json`, agentFiles.bundles['a'.repeat(64)]],
  ])
  const reads: string[] = []
  on('fs.list', (_$, e) => ({ value: e.path.replace(/\\/g, '/').endsWith('/.orchestrator')
    ? [{ name: 'config.toml', kind: 'file', size: selectedConfig.length, mtimeMs: 1, isLink: false }]
    : e.path.replace(/\\/g, '/').endsWith('/async_jobs') ? agentFiles.indexes.map(index => ({
    name: `${index.jobId}.json`, kind: 'file', size: 100, mtimeMs: now, isLink: false,
  })) : [] }))
  on('fs.read', (_$, e) => {
    if (e.path.replace(/\\/g, '/').endsWith('/.orchestrator/config.toml')) {
      reads.push('config.toml')
      return { value: selectedConfig }
    }
    const path = e.path.replace(/\\/g, '/').split('/purser/')[1]
    reads.push(path)
    return records[path] ? { value: JSON.stringify(records[path]) } : { deny: 'ENOENT' }
  })
  on('ui.render', { component: 'AbovePrompt' }, ($, e) => {
    const { Text } = $.ui.resolve(e)
    return Text({ children: 'base prompt' })
  })
  registerUsage(on)
  registerBoard(on)
  const ui = await $.ui.mount({
    plugin: 'usage-agents-test', surface: 'terminal', component: 'AbovePrompt',
    props: { hasSurvey: false, isWorking: false, maxRows: 8, bodyColumns: 100, scroll: { offset: 0, bodyRows: 8 }, view: {} },
  })
  expect(await ui.find({ type: 'Text', text: /^coder codex$/ })).toBeDefined()
  expect(await ui.find({ type: 'Text', text: /reviewer agy · gemini-3.8-flash-high/ })).toBeDefined()
  expect(await ui.find({ type: 'Text', text: /free opencode · beam-free/ })).toBeDefined()
  expect(reads).toHaveLength(7) // Existing board reads plus one user config read.
  await ui.unmount()
})

test('shows the selected reviewer agent alone when no model is selected', () => {
  const selected = parseSelectedAgents('[roles]\nreviewer = "agy"\n[reviewer]\nselected = { agent = "codex" }')
  expect(usageGroups({}, { quota: {} }, now, undefined, selected)[0][0]).toMatchObject({ id: 'reviewer', value: 'codex' })
})

test('omits a reviewer turned off by roles even with a selected reviewer', () => {
  const selected = parseSelectedAgents(selectedConfig.replace('reviewer = "claude"', 'reviewer = "none"'))
  expect(usageGroups({}, { quota: {} }, now, undefined, selected)[0].map(pill => pill.id)).toEqual(['coder'])
})

test('takes the coder from coding config and omits conflicting roles', () => {
  expect(parseSelectedAgents('[roles]\ncoder = "claude"\n[coding]\ncoder_agent = "claude"').coder).toBe('claude')
  expect(parseSelectedAgents('[roles]\ncoder = "claude"').coder).toBeUndefined()
  expect(parseSelectedAgents('[roles]\ncoder = "codex"\n[coding]\ncoder_agent = "claude"').coder).toBeUndefined()
})

test('omits selections for a missing config file', async ($, on) => {
  mock.env(on, { OS: 'Linux', HOME: '/missing-config' })
  on('fs.list', () => ({ deny: 'ENOENT' }))
  on('fs.read', () => { throw new Error('must not read a missing config') })
  const selected = await readSelectedAgents($)
  expect(selected).toEqual({})
  expect(usageGroups({}, { quota: {} }, now, undefined, selected)).toEqual([])
})

test('bounds config parsing and omits unsupported or duplicate selections', () => {
  for (const raw of ['#'.repeat(65537), '[roles]\ncoder = "codex"\ncoder = "claude"', '[reviewer]\nselected = { agent = "agy", model = invalid }']) {
    expect(parseSelectedAgents(raw)).toEqual({})
  }
})

test('skips an unrelated multiline array while preserving selected agents', () => {
  const raw = '[files]\r\npaths = [\r\n "a", # first path\r\n ["b"]\r\n]\r\n' + selectedConfig
  expect(parseSelectedAgents(raw)).toEqual({ coder: 'codex', reviewer: 'agy', model: 'gemini-3.8-flash-high' })
})

test('skips unrelated multiline basic string contents including apparent role keys', () => {
  const raw = '[notes]\ntext = """\n[roles]\ncoder = "claude"\n[reviewer]\nselected = { agent = "codex" }\n"""\n' + selectedConfig
  expect(parseSelectedAgents(raw)).toEqual({ coder: 'codex', reviewer: 'agy', model: 'gemini-3.8-flash-high' })
})

test('omits only the reviewer pill when its selected table is malformed', () => {
  const selected = parseSelectedAgents(selectedConfig.replace('model = "gemini-3.8-flash-high"', 'model = invalid'))
  expect(usageGroups({}, { quota: {} }, now, undefined, selected)[0].map(pill => [pill.id, pill.value])).toEqual([['coder', 'codex']])
})

test('uses platform home paths, caches unchanged config, and omits locked or oversized reads', async () => {
  for (const [os, home] of [['Windows_NT', 'C:\\Users\\pill-test\\'], ['Darwin', '/Users/pill-test/'], ['Linux', '/home/pill-test/']]) {
    let reads = 0
    let mtime = 1
    let size = selectedConfig.length
    let locked = false
    const paths: string[] = []
    const api = {
      env: { get: async (name: string) => name === 'OS' ? os : name === (os === 'Windows_NT' ? 'USERPROFILE' : 'HOME') ? home : '/wrong-home' },
      fs: {
        list: async () => [{ name: 'config.toml', kind: 'file', size, mtimeMs: mtime }],
        read: async (path: string) => {
          reads++
          paths.push(path)
          if (locked) throw new Error('ERROR_SHARING_VIOLATION')
          return selectedConfig
        },
      },
    }
    expect((await readSelectedAgents(api)).coder).toBe('codex')
    expect((await readSelectedAgents(api)).reviewer).toBe('agy')
    expect(reads).toBe(1)
    expect(paths).toEqual([`${home.replace(/[\\/]+$/, '')}/.orchestrator/config.toml`])
    mtime++
    locked = true
    expect(await readSelectedAgents(api)).toEqual({})
    expect(reads).toBe(2)
    size = 65537
    expect(await readSelectedAgents(api)).toEqual({})
    expect(reads).toBe(2)
  }
})

test('omits free when OpenCode has not run in the last seven days', () => {
  const files = { ...agentFiles, jobs: agentFiles.jobs.map(job => job.activeAgent === 'opencode' ? { ...job, startedAt: now / 1000 - 7 * 86400 - 1 } : job) }
  expect(usageGroups({}, { quota: {} }, now, files)).toEqual([])
})

test('formats compact counts and applies all quota bar thresholds', () => {
  expect(formatUsageNumber(15_600)).toBe('15.6k')
  expect(formatUsageNumber(954_200)).toBe('954.2k')
  expect(formatUsageNumber(1_250_000)).toBe('1.25M')
  expect([69, 70, 89, 90].map(barColor)).toEqual(['#279b69', '#d6a900', '#d6a900', '#dc4c4c'])
})

test('places the time-elapsed marker from window elapsed time', () => {
  expect(timeElapsedTick(2.5 * 60 * 60_000, 5 * 60 * 60_000)).toBe(0.5)
  expect(timeElapsedTick(10, 5)).toBe(0)
  expect(timeElapsedTick(-5, 5)).toBe(1)
})

test('maps session usage fields and keeps Claude, Codex, then cost groups', () => {
  const session = mapSessionUsage(usage, now)
  const groups = usageGroups(readQuotaSnapshot([quotaRecord()], now), session, now)
  expect(session.contextFill).toBe(42.5)
  expect(session.cost).toBe(4.32)
  expect(groups.map(group => group.map(pill => pill.id))).toEqual([
    ['claude-5h', 'claude-7d', 'claude-context'], ['codex-5h', 'codex-7d'], ['cost'],
  ])
  expect(groups[0][0].detail).toContain('context 42.5%')
  expect(groups[0][2]).toMatchObject({ value: '43%', percent: 42.5 })
  expect(terminalBar(groups[0][2])).toBe('████░░░░░░')
  expect(groups[2][0].detail).toContain('summed over priced API responses')
  expect(groups.flat().some(pill => ['input', 'output', 'cache'].includes(pill.id))).toBe(false)
})

test('uses brand colors and marks the first Claude and Codex pill in SVG', () => {
  const session = mapSessionUsage(usage, now)
  const groups = usageGroups({ ...readQuotaSnapshot([quotaRecord()], now), model: 'gpt-6-luna' }, session, now)
  expect(groups[0].map(pill => pill.color)).toEqual(['#d97757', '#d97757', '#d97757'])
  expect(groups[1].map(pill => pill.color)).toEqual(['#6b7cff', '#6b7cff', '#6b7cff'])
  expect(groups[2][0].color).toBe('#bd922e')
  expect(pillSvg(groups[0][0])).toContain('data-brand-mark="claude"')
  expect(pillSvg(groups[1][0])).toContain('data-brand-mark="codex"')
  expect(pillSvg(groups[0][1])).not.toContain('data-brand-mark=')
  expect(pillSvg(groups[1][1])).not.toContain('data-brand-mark=')
})

test('hides cost and Claude quota pills when session usage omits their sources', () => {
  const session = mapSessionUsage({ context: { window: 200_000 }, rateLimits: [] }, now)
  expect(session.cost).toBeUndefined()
  expect(session.contextFill).toBeUndefined()
  expect(session.quota).toEqual({ fiveHour: undefined, sevenDay: undefined })
  expect(usageGroups({}, session, now)).toEqual([])
})

test('selects the newest Codex artifact with quota and stops at twenty files', () => {
  const found = readQuotaSnapshot([{}, JSON.stringify(quotaRecord(55, 65)), quotaRecord(75, 85)], now)
  expect(found.fiveHour?.percent).toBe(55)
  expect(readQuotaSnapshot(Array.from({ length: 20 }, () => ({})).concat([quotaRecord()]), now)).toEqual({})
})

test('shows reset windows as renewed and keeps invalid windows hidden', () => {
  const expired = quotaRecord(69, 90)
  expired.codex_quota['5h_resets_at'] = '2026-10-04T11:59:59Z'
  const codex = readQuotaSnapshot([expired], now)
  const claude = mapSessionUsage({ context: { percent: 42.5 }, rateLimits: [
    { kind: 'five_hour', percentUsed: 20, resetsAt: '2026-10-04T11:59:59Z' },
    { kind: 'seven_day', percentUsed: 30, resetsAt: '2026-10-04T14:00:00Z' },
  ] }, now)
  const codexPills = usageGroups(codex, { quota: { fiveHour: undefined, sevenDay: undefined } }, now)[0]
  const codexPill = codexPills[0]
  const claudePill = usageGroups({}, claude, now)[0][0]
  expect(codexPill).toMatchObject({
    id: 'codex-5h', value: '0%', percent: 0,
    detail: 'codex 5h quota renewed; no usage recorded since the reset',
  })
  expect(codexPill.tick).toBeUndefined()
  expect(codexPill.countdown).toBeUndefined()
  expect(codexPills.map(pill => pill.id)).toEqual(['codex-5h', 'codex-7d'])
  expect(claudePill).toMatchObject({
    id: 'claude-5h', value: '0%', percent: 0,
    detail: 'claude 5h quota renewed; no usage recorded since the reset; context 42.5%',
  })
  expect(claudePill.tick).toBeUndefined()
  expect(claudePill.countdown).toBeUndefined()

  const invalid = quotaRecord()
  invalid.codex_quota['5h_used_pct'] = 101
  invalid.codex_quota['5h_resets_at'] = 'not a reset date'
  invalid.codex_quota['7d_used_pct'] = Number.NaN
  const invalidPills = usageGroups(readQuotaSnapshot([invalid], now), { quota: { fiveHour: undefined, sevenDay: undefined } }, now)
  expect(invalidPills).toEqual([])

  const futurePill = usageGroups(readQuotaSnapshot([quotaRecord()], now), { quota: { fiveHour: undefined, sevenDay: undefined } }, now)[0][0]
  expect(futurePill).toMatchObject({ id: 'codex-5h', value: '69%', percent: 69, countdown: '2h 30m', tick: 0.5 })
  expect(futurePill.detail).toBe('codex 5h quota 69.0% used; resets in 2h 30m')
})

test('uses a dark theme media query and an interactive title in each SVG', () => {
  const pill = usageGroups({}, mapSessionUsage(usage, now), now)[0][0]
  const svg = pillSvg(pill)
  expect(svg).toContain('@media(prefers-color-scheme:dark)')
  expect(svg).toContain('<title>')
  expect(svg).toContain('2h 40m')
  expect(svg).toContain('stroke-width="1.4"')
})

test('steps aside for surveys before reading usage or artifacts', async ($, on) => {
  mock.clock(on)
  let read = false
  on('session.usage', () => { read = true; return { deny: 'unexpected usage read' } })
  on('fs.list', () => { read = true; return { deny: 'unexpected read' } })
  on('ui.render', { component: 'AbovePrompt' }, ($, e) => {
    const { Text } = $.ui.resolve(e)
    return Text({ children: 'base prompt' })
  })
  registerUsage(on)
  const ui = await $.ui.mount({
    plugin: 'usage-survey-test', surface: 'terminal', component: 'AbovePrompt',
    props: { hasSurvey: true, isWorking: false, maxRows: 8, bodyColumns: 80, scroll: { offset: 0, bodyRows: 8 }, view: {} },
  })
  expect(read).toBe(false)
  await ui.unmount()
})

test('draws valid Svg pills on desktop and keeps another mod drawing', async ($, on) => {
  mock.clock(on)
  mock.env(on, { OS: 'Linux', HOME: '/home/test' })
  on('session.usage', () => ({ value: usage }))
  on('fs.list', () => ({ deny: 'ENOENT' }))
  on('ui.render', { component: 'AbovePrompt' }, ($, e) => {
    const { Text } = $.ui.resolve(e)
    return Text({ children: 'Other mod status' })
  })
  registerUsage(on)
  const ui = await $.ui.mount({
    plugin: 'usage-desktop-test', surface: 'desktop', component: 'AbovePrompt',
    props: { hasSurvey: false, isWorking: false, maxRows: 8, bodyColumns: 100, scroll: { offset: 0, bodyRows: 8 }, view: {} },
  })
  expect((await ui.find({ type: 'Text', text: 'Other mod status' }))?.text).toBe('Other mod status')
  const svg = await ui.find({ type: 'Svg' })
  expect(svg?.props.source.startsWith('<svg')).toBe(true)
  expect(svg?.props.alt.length > 0).toBe(true)
  await ui.unmount()
})

test('keeps another mod drawing and uses the terminal fallback', async ($, on) => {
  mock.clock(on)
  mock.env(on, { OS: 'Linux', HOME: '/home/test' })
  on('session.usage', () => ({ value: {
    ...usage,
    rateLimits: usage.rateLimits.map(limit => ({ ...limit, resetsAt: new Date(Date.now() + 60 * 60_000).toISOString() })),
  } }))
  on('fs.list', () => ({ deny: 'ENOENT' }))
  on('ui.render', { component: 'AbovePrompt' }, ($, e) => {
    const { Text } = $.ui.resolve(e)
    return Text({ children: 'Other mod status' })
  })
  registerUsage(on)
  const ui = await $.ui.mount({
    plugin: 'usage-terminal-test', surface: 'terminal', component: 'AbovePrompt',
    props: { hasSurvey: false, isWorking: false, maxRows: 8, bodyColumns: 100, scroll: { offset: 0, bodyRows: 8 }, view: {} },
  })
  expect((await ui.find({ type: 'Text', text: 'Other mod status' }))?.text).toBe('Other mod status')
  expect(await ui.find({ type: 'Svg' })).toBeUndefined()
  expect(await ui.find({ type: 'Text', text: /5h/ })).toBeDefined()
  expect(await ui.find({ type: 'Text', text: /✻/ })).toBeDefined()
  expect(await ui.find({ type: 'Text', text: /\$4.32/ })).toBeDefined()
  const claude = usageGroups({}, mapSessionUsage(usage, now), now)[0][0]
  expect(terminalBar(claude)).toContain('│')
  expect(terminalBar(claude)).toContain('█')
  expect(terminalBar(claude)).toContain('░')
  await ui.unmount()
})

test('reads Codex quota from the newest artifact that contains it', async ($, on) => {
  mock.clock(on)
  mock.env(on, { OS: 'Linux', HOME: '/home/test', PURSER_STATE_DIR: '/shared/purser-state' })
  on('session.usage', () => ({ value: { context: { window: 200_000 }, rateLimits: [] } }))
  const newest = `${'a'.repeat(64)}.json`
  const prior = `${'b'.repeat(64)}.json`
  const reads: string[] = []
  const listed: string[] = []
  on('fs.list', (_$, e) => {
    listed.push(e.path)
    return ({ value: [
    { name: newest, kind: 'file', size: 50, mtimeMs: 20, isLink: false },
    { name: prior, kind: 'file', size: 50, mtimeMs: 10, isLink: false },
    ] })
  })
  on('fs.read', (_$, e) => {
    reads.push(e.path)
    const snapshot = quotaRecord(55, 65)
    snapshot.codex_quota['5h_resets_at'] = new Date(Date.now() + 60 * 60_000).toISOString()
    snapshot.codex_quota['7d_resets_at'] = new Date(Date.now() + 24 * 60 * 60_000).toISOString()
    return { value: e.path.endsWith(newest) ? '{"incomplete":true}' : JSON.stringify(snapshot) }
  })
  on('ui.render', { component: 'AbovePrompt' }, ($, e) => {
    const { Text } = $.ui.resolve(e)
    return Text({ children: 'base prompt' })
  })
  registerUsage(on)
  const ui = await $.ui.mount({
    plugin: 'usage-codex-test', surface: 'terminal', component: 'AbovePrompt',
    props: { hasSurvey: false, isWorking: false, maxRows: 8, bodyColumns: 100, scroll: { offset: 0, bodyRows: 8 }, view: {} },
  })
  expect(listed).toHaveLength(2)
  expect(listed.some(path => path.replace(/\\/g, '/').toLowerCase().endsWith('/shared/purser-state/bundle_artifacts'))).toBe(true)
  expect(reads.map(path => path.split(/[\\/]/).at(-1))).toEqual([newest, prior])
  expect(await ui.find({ type: 'Text', text: /codex 5h/ })).toBeDefined()
  expect(await ui.find({ type: 'Text', text: />_/ })).toBeDefined()
  await ui.unmount()
})

test('shows the latest Codex model and effort as the first Codex pill', () => {
  const records = [JSON.stringify({ model: 'gpt-6-luna', effort: 'high' }), quotaRecord()]
  expect(readCodexModel(records)).toEqual({ model: 'gpt-6-luna', effort: 'high' })
  expect(readCodexModel(['not json', { model: '' }])).toEqual({})
  const groups = usageGroups({ ...readQuotaSnapshot([quotaRecord()], now), ...readCodexModel(records) }, mapSessionUsage(usage, now), now)
  expect(groups[1][0]).toMatchObject({ id: 'codex-model', label: 'codex', value: 'gpt-6-luna · high', color: '#6b7cff' })
  expect(terminalBar(groups[1][0])).toBeUndefined()
})
