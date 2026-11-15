import { stateRoot } from './state-root.js'
import { cachedProjectJobs } from './board.js'
import type { BoardFiles } from './board.js'

type Window = { percent: number; resetAt?: number; windowMs: number }
type Quota = { fiveHour?: Window; sevenDay?: Window; model?: string; effort?: string }
type SessionData = { quota: Quota; contextFill?: number; cost?: number }
type SelectedAgents = { coder?: string; reviewer?: string; model?: string }
type Pill = { id: string; label: string; value: string; detail: string; icon: string; color: string; brandMark?: 'claude' | 'codex'; percent?: number; tick?: number; countdown?: string }

const finite = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value)
const agentColors = new Map([
  ['claude', '#D97757'], ['codex', '#10A37F'], ['gemini', '#8E75B2'],
  ['agy', '#8E75B2'], ['opencode', '#0D9488'], ['cursor', '#2563EB'],
])

function quotaWindow(percent: unknown, reset: unknown, windowMs: number, now: number): Window | undefined {
  const resetAt = typeof reset === 'string' ? Date.parse(reset) : Number.NaN
  if (!finite(percent) || percent < 0 || percent > 100 || !Number.isFinite(resetAt)) return undefined
  return resetAt <= now ? { percent: 0, windowMs } : { percent, resetAt, windowMs }
}

export function formatUsageNumber(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(value >= 10_000_000 ? 1 : 2).replace(/\.?0+$/, '')}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(1).replace(/\.?0+$/, '')}k`
  return String(value)
}

export function barColor(percent: number): string {
  return percent >= 90 ? '#dc4c4c' : percent >= 70 ? '#d6a900' : '#279b69'
}

export function timeElapsedTick(timeLeft: number, windowLength: number): number {
  return Math.max(0, Math.min(1, 1 - timeLeft / windowLength))
}

const remaining = (resetAt: number, now: number) => {
  const minutes = Math.max(0, Math.ceil((resetAt - now) / 60_000))
  const days = Math.floor(minutes / 1440)
  const hours = Math.floor((minutes % 1440) / 60)
  const mins = minutes % 60
  return days ? `${days}d ${hours}h` : hours ? `${hours}h ${mins}m` : `${mins}m`
}

function rateLimit(rateLimits: any, kind: 'five_hour' | 'seven_day', windowMs: number, now: number): Window | undefined {
  const limit = Array.isArray(rateLimits) ? rateLimits.find(item => item?.kind === kind) : undefined
  return quotaWindow(limit?.percentUsed, limit?.resetsAt, windowMs, now)
}

export function mapSessionUsage(value: any, now = Date.now()): SessionData {
  const contextFill = finite(value?.context?.percent) ? value.context.percent : undefined
  const cost = finite(value?.cost?.usd) && value.cost.usd >= 0 ? value.cost.usd : undefined
  return {
    quota: {
      fiveHour: rateLimit(value?.rateLimits, 'five_hour', 5 * 60 * 60_000, now),
      sevenDay: rateLimit(value?.rateLimits, 'seven_day', 7 * 24 * 60 * 60_000, now),
    },
    contextFill,
    cost,
  }
}

export function claudeRateLimitResetAt(value: any, now = Date.now()): number | undefined {
  const windows = mapSessionUsage(value, now).quota
  const resets = [windows.fiveHour, windows.sevenDay]
    .filter((window): window is Window => window !== undefined && window.percent >= 100)
    .map(window => window.resetAt)
  return resets.length ? Math.max(...resets) : undefined
}

export function readCodexModel(records: unknown[]): Pick<Quota, 'model' | 'effort'> {
  for (const value of records.slice(0, 20)) {
    let item: any = value
    try { if (typeof value === 'string') item = JSON.parse(value) } catch { continue }
    if (typeof item?.model === 'string' && item.model) {
      return { model: item.model, effort: typeof item.effort === 'string' && item.effort ? item.effort : undefined }
    }
  }
  return {}
}

export function readQuotaSnapshot(records: unknown[], now = Date.now()): Quota {
  for (const value of records.slice(0, 20)) {
    let item = value
    try { if (typeof value === 'string') item = JSON.parse(value) } catch { continue }
    const quota = (item as any)?.codex_quota
    if (!quota || typeof quota !== 'object') continue
    const window = (name: '5h' | '7d', duration: number): Window | undefined => {
      return quotaWindow(quota[`${name}_used_pct`], quota[`${name}_resets_at`], duration, now)
    }
    return { fiveHour: window('5h', 5 * 60 * 60_000), sevenDay: window('7d', 7 * 24 * 60 * 60_000) }
  }
  return {}
}

function quotaPills(quota: Quota, prefix: string, now: number, contextFill?: number): Pill[] {
  const pills: Pill[] = []
  for (const [name, window, icon] of [
    ['5h', quota.fiveHour, '◴'], ['7d', quota.sevenDay, '▦'],
  ] as const) {
    if (!window) continue
    const renewed = window.resetAt === undefined
    const timeLeft = renewed ? undefined : window.resetAt - now
    const context = contextFill === undefined ? '' : `; context ${contextFill.toFixed(1)}%`
    pills.push({
      id: `${prefix}-${name}`, label: prefix === 'codex' ? `codex ${name}` : `${icon} ${name}`,
      value: `${Math.round(window.percent)}%`,
      detail: renewed
        ? `${prefix} ${name} quota renewed; no usage recorded since the reset${context}`
        : `${prefix} ${name} quota ${window.percent.toFixed(1)}% used; resets in ${remaining(window.resetAt!, now)}${context}`,
      icon: '◷', color: prefix === 'claude' ? '#d97757' : '#6b7cff', percent: window.percent,
      ...(timeLeft === undefined ? {} : { tick: timeElapsedTick(timeLeft, window.windowMs), countdown: remaining(window.resetAt!, now) }),
    })
  }
  return pills
}

export function usageGroups(codex: Quota, session: SessionData, now = Date.now(), files?: BoardFiles, selected: SelectedAgents = {}): Pill[][] {
  const claude = quotaPills(session.quota, 'claude', now, session.contextFill)
  if (session.contextFill !== undefined) {
    claude.push({
      id: 'claude-context', label: '◧ ctx', value: `${Math.round(session.contextFill)}%`,
      detail: `claude context ${session.contextFill.toFixed(1)}% of the window used`,
      icon: '◧', color: '#d97757', percent: session.contextFill,
    })
  }
  if (claude.length) claude[0].brandMark = 'claude'
  const codexPills = quotaPills(codex, 'codex', now, session.contextFill)
  if (codexPills.length) codexPills[0].brandMark = 'codex'
  const groups: Pill[][] = [claude, codexPills]
  const jobs = (files?.jobs ?? []).filter(job => finite(job.startedAt)).map(job => ({
    ...job, agent: job.servedAgent ?? job.activeAgent, model: job.servedAgent ? job.servedModel : job.model,
  }))
    .sort((a, b) => b.startedAt - a.startedAt)
  const free = jobs.find(job => job.agent === 'opencode' && job.startedAt * 1000 <= now && now - job.startedAt * 1000 <= 7 * 24 * 60 * 60_000)
  const coderModel = selected.coder?.toLowerCase() === 'codex' && codex.model ? `${codex.model}${codex.effort ? ` · ${codex.effort}` : ''}` : undefined
  const agents: Pill[] = []
  for (const [role, agent, model, detail] of [
    ['coder', selected.coder, undefined, `Coder selected for Purser${coderModel ? `; latest model ${codex.model}` : ''}`],
    ['reviewer', selected.reviewer, selected.model, 'Reviewer selected for Purser'],
    ['free', free?.agent, free?.model, 'OpenCode Purser used in the last 7 days'],
  ]) {
    if (typeof agent !== 'string' || !agent.trim() || ['unknown', 'none'].includes(agent.toLowerCase()) ||
        (role === 'free' && !model) || (model !== undefined && (typeof model !== 'string' || !model.trim() || model.toLowerCase() === 'unknown'))) continue
    const value = role === 'coder' && coderModel ? coderModel : model ? `${agent} · ${model.startsWith(`${agent}/`) ? model.slice(agent.length + 1) : model}` : agent
    agents.push({ id: role, label: role, value, detail, icon: '', color: agentColors.get(agent.toLowerCase()) ?? '#788797' })
  }
  groups.push(agents)
  if (session.cost !== undefined) {
    const context = session.contextFill === undefined ? '' : `; context ${session.contextFill.toFixed(1)}%`
    groups.push([{
      id: 'cost', label: '$ session', value: `$${session.cost.toFixed(2)}`,
      detail: `$${session.cost.toFixed(2)} session cost, summed over priced API responses${context}`,
      icon: '$', color: '#bd922e',
    }])
  }
  return groups.filter(group => group.length > 0)
}

const escapeXml = (value: string) => value.replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&apos;' })[character]!)

// Monospace advance per character at the given font size; lays each part out after the previous one.
const textWidth = (value: string, size: number) => Math.ceil([...value].length * size * 0.62)

export function pillSvg(pill: Pill, maxWidth = Infinity): string {
  const style = `<style>text{font-family:ui-monospace,SFMono-Regular,monospace;fill:#27313b}.pill{fill:${pill.color}22}@media(prefers-color-scheme:dark){text{fill:#f3f5f7}.pill{fill:${pill.color}55}}</style>`
  const height = 24
  const baseline = 16
  let x = 10
  let brandMark = ''
  if (pill.brandMark === 'claude') {
    brandMark = `<g data-brand-mark="claude" transform="translate(${x},6)" stroke="${pill.color}" stroke-width="1.5" stroke-linecap="round"><path d="M6 0v12M0 6h12M1.76 1.76l8.48 8.48M10.24 1.76l-8.48 8.48"/></g>`
  } else if (pill.brandMark === 'codex') {
    brandMark = `<g data-brand-mark="codex" transform="translate(${x},6)" fill="none" stroke="${pill.color}" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M1 1l5 5-5 5M8 10h4"/></g>`
  }
  if (pill.brandMark) x += Math.max(12, textWidth(pill.brandMark === 'claude' ? '✻' : '>_', 12)) + 4
  let inner = `<text x="${x}" y="${baseline}" font-size="12">${escapeXml(pill.label)}</text>`
  inner = brandMark + inner
  x += textWidth(pill.label, 12) + 8
  if (pill.percent !== undefined) {
    const barWidth = 46
    inner += `<rect x="${x}" y="10" width="${barWidth}" height="4" rx="2" fill="#8c96a0" opacity=".35"/><rect x="${x}" y="10" width="${barWidth * pill.percent / 100}" height="4" rx="2" fill="${barColor(pill.percent)}"/>${pill.tick === undefined ? '' : `<path d="M${x + barWidth * pill.tick} 7v10" stroke="#252b32" stroke-width="1.4"/>`}`
    x += barWidth + 8
  }
  const countdown = pill.countdown && x + textWidth(pill.value, 12) + 24 + textWidth(`${pill.icon} ${pill.countdown}`, 11) <= maxWidth ? pill.countdown : undefined
  const valueWidth = maxWidth - x - 10 - (countdown ? 14 + textWidth(`${pill.icon} ${countdown}`, 11) : 0)
  const characters = Math.max(0, Math.floor(valueWidth / (12 * 0.62)))
  const value = [...pill.value].length > characters ? `${[...pill.value].slice(0, Math.max(0, characters - 1)).join('')}${characters ? '…' : ''}` : pill.value
  inner += `<text x="${x}" y="${baseline}" font-size="12" font-weight="700">${escapeXml(value)}</text>`
  x += textWidth(value, 12)
  if (countdown) {
    x += 7
    inner += `<path d="M${x} 5v14" stroke="#66717c" opacity=".5"/>`
    x += 7
    inner += `<text x="${x}" y="${baseline}" font-size="11">${escapeXml(pill.icon)} ${escapeXml(countdown)}</text>`
    x += textWidth(`${pill.icon} ${countdown}`, 11)
  }
  const width = x + 10
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}">${style}<title>${escapeXml(pill.detail)}</title><rect class="pill" x=".5" y=".5" width="${width - 1}" height="${height - 1}" rx="${(height - 1) / 2}"/>${inner}</svg>`
}

async function readCodexQuota($, root: string, now: number): Promise<Quota> {
  try {
    const dir = `${root.replace(/[\\/]+$/, '')}/bundle_artifacts`
    const files = (await $.fs.list(dir)).filter(item => item.kind === 'file' && /^[0-9a-f]{64}\.json$/.test(item.name))
      .sort((a, b) => b.mtimeMs - a.mtimeMs).slice(0, 20)
    const records = []
    for (const file of files) {
      const content = await $.fs.read(`${dir}/${file.name}`)
      records.push(content)
      try { if (JSON.parse(content)?.codex_quota) break } catch { /* Try the next artifact. */ }
    }
    return { ...readQuotaSnapshot(records, now), ...readCodexModel(records) }
  } catch { return {} }
}

// Only role keys need decoding; unrelated multiline values are skipped.
export function parseSelectedAgents(raw: string): SelectedAgents {
  if (raw.length > 65536 || new TextEncoder().encode(raw).length > 65536) return {}
  const values = new Map<string, string>()
  const invalid = new Set<string>()
  let section = ''
  let quote = ''
  let depth = 0
  const string = (value: string) => /^(?:"([A-Za-z0-9._:/-]+)"|'([A-Za-z0-9._:/-]+)')$/.exec(value.trim())
  for (const rawLine of raw.split(/\r?\n/)) {
    const continuation = depth > 0 || quote.length === 3
    let line = rawLine
    for (let i = 0; i < rawLine.length; i++) {
      const character = rawLine[i]
      if (quote) {
        if (quote[0] === '"' && character === '\\') { i++; continue }
        if (rawLine.startsWith(quote, i)) { i += quote.length - 1; quote = '' }
      } else if (character === '#') {
        line = rawLine.slice(0, i)
        break
      } else if (character === '"' || character === "'") {
        quote = rawLine.startsWith(character.repeat(3), i) ? character.repeat(3) : character
        i += quote.length - 1
      } else if (character === '[') depth++
      else if (character === ']') depth = Math.max(0, depth - 1)
    }
    if (quote.length === 1) quote = ''
    line = line.trim()
    if (continuation || !line) continue
    if (line.startsWith('[')) {
      const header = /^\[([A-Za-z0-9_.-]+)\]$/.exec(line)
      section = header?.[1] ?? ''
      continue
    }
    const entry = /^([A-Za-z0-9_]+)\s*=\s*(.*)$/.exec(line)
    const name = entry?.[1] ?? /^([A-Za-z0-9_]+)\b/.exec(line)?.[1]
    const key = `${section}.${name}`
    const rawValue = entry?.[2] ?? ''
    if (key === 'reviewer.selected') {
      if (!/^\{.*\}$/.test(rawValue)) { invalid.add('reviewer'); continue }
      for (const field of rawValue.slice(1, -1).split(',')) {
        const pair = /^\s*(agent|model|effort)\s*=\s*(.+?)\s*$/.exec(field)
        if (!pair || values.has(`reviewer.selected.${pair[1]}`)) { invalid.add('reviewer'); break }
        const value = string(pair[2])
        if (!value) { invalid.add('reviewer'); break }
        values.set(`reviewer.selected.${pair[1]}`, value[1] ?? value[2])
      }
    } else if (['roles.coder', 'roles.reviewer', 'coding.coder_agent', 'coding.reviewer1_agent', 'coding.reviewer1_model_requested', 'reviewer.selected.agent', 'reviewer.selected.model'].includes(key)) {
      const value = string(rawValue)
      if (!value || values.has(key)) { invalid.add(key === 'roles.coder' || key === 'coding.coder_agent' ? 'coder' : 'reviewer'); continue }
      values.set(key, value[1] ?? value[2])
    }
  }
  const agents = ['codex', 'agy', 'claude', 'opencode', 'cursor']
  const role = values.get('roles.coder')
  const coder = values.get('coding.coder_agent') ?? (role === 'codex' ? role : undefined)
  const selected: SelectedAgents = {}
  if (!invalid.has('coder') && coder && agents.includes(coder) && (role ?? 'codex') === coder) selected.coder = coder
  if (invalid.has('reviewer') || values.get('roles.reviewer') === 'none') return selected
  const reviewer = values.get('reviewer.selected.agent') ?? values.get('coding.reviewer1_agent') ?? values.get('roles.reviewer')
  if (reviewer && agents.includes(reviewer)) {
    selected.reviewer = reviewer
    selected.model = values.get('reviewer.selected.model') ?? values.get('coding.reviewer1_model_requested')
  }
  return selected
}

let selectedCache: { path: string; mtime: number; size: number; selected: SelectedAgents } | undefined

export async function readSelectedAgents($): Promise<SelectedAgents> {
  try {
    const os = await $.env.get('OS')
    const home = os === 'Windows_NT' ? await $.env.get('USERPROFILE') : await $.env.get('HOME')
    if (!home) return {}
    const dir = `${home.replace(/[\\/]+$/, '')}/.orchestrator`
    const entry = (await $.fs.list(dir)).find(entry => entry.name === 'config.toml' && entry.kind === 'file')
    if (!entry || !finite(entry.size) || entry.size < 0 || entry.size > 65536) return {}
    const path = `${dir}/config.toml`
    if (finite(entry.mtimeMs) && selectedCache?.path === path && selectedCache.mtime === entry.mtimeMs && selectedCache.size === entry.size) return selectedCache.selected
    const selected = parseSelectedAgents(await $.fs.read(path))
    selectedCache = finite(entry.mtimeMs) ? { path, mtime: entry.mtimeMs, size: entry.size, selected } : undefined
    return selected
  } catch { selectedCache = undefined; return {} }
}

async function runtimeStateRoot($): Promise<string | undefined> {
  const os = await $.env.get('OS')
  const [purserState, claudeConfig, home] = await Promise.all([
    $.env.get('PURSER_STATE_DIR'),
    $.env.get('CLAUDE_CONFIG_DIR'),
    os === 'Windows_NT' ? $.env.get('USERPROFILE') : $.env.get('HOME'),
  ])
  return stateRoot({ purserState, claudeConfig, home })
}

export function terminalBar(pill: Pill): string | undefined {
  if (pill.percent === undefined) return undefined
  const width = 10
  const full = Math.round(width * pill.percent / 100)
  const tick = pill.tick === undefined ? -1 : Math.round(width * pill.tick)
  return Array.from({ length: width }, (_, i) => i === tick ? '│' : i < full ? '█' : '░').join('')
}

function terminalPill(Text, pill: Pill) {
  const bar = terminalBar(pill)
  const mark = pill.brandMark === 'claude' ? '✻' : pill.brandMark === 'codex' ? '>_' : undefined
  return Text({ color: pill.color, children: [...(mark ? [mark, ' '] : []), pill.label, ' ', ...(bar ? [bar, ' '] : []), pill.value, ...(pill.countdown ? [' │ ◷ ', pill.countdown] : [])] })
}

let timerStarted = false
let selectedForBar: SelectedAgents = {}
let selectionPending = false

export function registerUsage(on) {
  on('session.measure', async ($, e, next) => {
    const result = await next(e)
    $.ui.invalidate('ui.render')
    return result
  })
  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    if (e.props.hasSurvey) return next(e)
    const prior = await next(e)
    try {
      if (!timerStarted) {
        timerStarted = true
        $.clock.every(5000, () => $.ui.invalidate('ui.render'))
      }
      // Optional user-file reads must not hold the quota band behind a locked file.
      if (!selectionPending) {
        selectionPending = true
        void readSelectedAgents($).then(selected => {
          const changed = selected.coder !== selectedForBar.coder || selected.reviewer !== selectedForBar.reviewer || selected.model !== selectedForBar.model
          selectedForBar = selected
          selectionPending = false
          if (changed) $.ui.invalidate('ui.render')
        }).catch(() => { selectionPending = false })
      }
      const [usage, root] = await Promise.all([$.session.usage().catch(() => ({})), runtimeStateRoot($).catch(() => undefined)])
      const session = mapSessionUsage(usage)
      const codex = root ? await readCodexQuota($, root, Date.now()) : {}
      const cwd = await $.session.cwd().catch(() => undefined)
      const groups = usageGroups(codex, session, Date.now(), cwd === undefined ? undefined : cachedProjectJobs(cwd), selectedForBar)
      if (!groups.length) return prior
      const { Box, Text, Svg } = $.ui.resolve(e)
      const terminal = e.surface === 'terminal' || !Svg
      const rendered = groups.flat().map(pill => Box({
        key: pill.id, flexShrink: 0,
        children: terminal ? terminalPill(Text, pill) : Svg({ key: pill.id, source: pillSvg(pill, e.props.bodyColumns * 12 * 0.62), alt: pill.detail }),
      }))
      const band = Box({ key: 'purser-usage-band', width: e.props.bodyColumns, flexDirection: 'row', flexWrap: 'wrap', gap: terminal ? 3 : 1, children: rendered })
      return Box({ key: 'purser-usage', flexDirection: 'column', children: prior ? [prior, band] : [band] })
    } catch { return prior }
  })
}
