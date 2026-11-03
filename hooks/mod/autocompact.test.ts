import { expect, mock, test } from 'claude-code/testing'
import { compactNotice, DEFAULT_THRESHOLD, needsCompactPrompt, resetAutoCompactForTests, RESUME_PROMPT, thresholdFrom, turnIsRunning, WAKE_MARKER, wakeContext } from './autocompact.js'

const TURN = { answer: 'ok', durationMs: 10, isAborted: false, turnId: 't1', reason: 'answer' } as const
const normalized = (path: string) => path.replace(/\\/g, '/').replace(/(^|:)[A-Za-z]:(?=\/)/g, '$1')

function engine(on, tokens: number, env: Record<string, string> = {}, failures = 0, setting: string | null = '{"enabled": true}', persistWrites = false) {
  resetAutoCompactForTests()
  const calls = { compacts: 0 }
  const reads: string[] = []
  const logs: string[] = []
  const files = new Map<string, string>()
  let sessionId = 'session-1'
  const clock = mock.clock(on)
  mock.env(on, { OS: 'Linux', HOME: '/home', ...env })
  on('fs.read', async (_$, e) => {
    if (/auto-compact.log$/.test(e.path)) {
      return { value: files.get(e.path) ?? '' }
    }
    if (setting === null || !/auto-compact.json$/.test(e.path)) throw new Error('ENOENT')
    reads.push(e.path)
    return { value: setting }
  })
  if (persistWrites) on('fs.write', async (_$, e) => { files.set(e.path, e.text); return { value: undefined } })
  on('session.id', () => ({ value: sessionId }))
  on('turn.complete', async () => ({ text: 'ok' }))
  on('prompt.submit', async (_$, e) => ({ text: e.text, origin: e.origin }))
  on('ui.log', (_$, e) => { logs.push(e.text); return { value: undefined } })
  on('session.usage', async () => ({ value: { startedAt: 0, context: { tokens, window: 1_000_000 }, rateLimits: [] } }))
  on('session.compact', async (_$, e) => {
    calls.compacts++
    if (calls.compacts <= failures) throw new Error('a turn is running')
    return { messages: [{ role: 'user', text: 'compacted', toolUses: [] }] }
  })
  return { calls, clock, logs, reads, files, setSessionId: (id: string) => { sessionId = id } }
}

test('reads the threshold override and the off switch', () => {
  expect(thresholdFrom(undefined)).toBe(DEFAULT_THRESHOLD)
  expect(thresholdFrom('120000')).toBe(120_000)
  expect(thresholdFrom('junk')).toBe(DEFAULT_THRESHOLD)
  expect(thresholdFrom('0')).toBe(undefined)
  expect(thresholdFrom('off')).toBe(undefined)
})

test('compacts once after a main turn past the threshold', async ($, on) => {
  const { calls, clock } = engine(on, 260_000)
  await $.turn.complete(TURN)
  expect(calls.compacts).toBe(0)
  await clock.advance(1_000)
  expect(calls.compacts).toBe(1)
})

test('leaves turns under the threshold alone', async ($, on) => {
  const { calls, clock } = engine(on, 240_000)
  await $.turn.complete(TURN)
  await clock.advance(20_000)
  expect(calls.compacts).toBe(0)
})

test('arms the idle timer at 150K below the native idle threshold', async ($, on) => {
  const { calls, clock, files } = engine(on, 150_000, {}, 0, '{"enabled": true}', true)
  await $.turn.complete(TURN)
  expect([...files.values()].join('\n')).toContain('idle timer armed at 150000 tokens')
  await clock.advance(271_000)
  expect(calls.compacts).toBe(1)
})

test('yields the idle timer to native idle compaction at 200K', async ($, on) => {
  const { calls, clock, files } = engine(on, 200_000, {}, 0, '{"enabled": true}', true)
  on('settings.read', async () => ({ value: { idleCompaction: true } }))
  await $.turn.complete(TURN)
  const log = [...files.values()].join('\n')
  expect(log).toContain('idle timer yielded to native idle compaction at 200000 tokens')
  expect(log).not.toContain('idle timer armed')
  await clock.advance(271_000)
  expect(calls.compacts).toBe(0)
})

test('arms the idle timer above 200K when native idle compaction is disabled', async ($, on) => {
  const { calls, clock, files } = engine(on, 240_000, {}, 0, '{"enabled": true}', true)
  on('settings.read', async () => ({ value: { idleCompaction: false } }))
  await $.turn.complete(TURN)
  expect([...files.values()].join('\n')).toContain('idle timer armed at 240000 tokens')
  await clock.advance(271_000)
  expect(calls.compacts).toBe(1)
})

test('assumes native idle compaction is on when settings cannot be read', async ($, on) => {
  const { calls, clock, files } = engine(on, 240_000, {}, 0, '{"enabled": true}', true)
  on('settings.read', async () => { throw new Error('settings unavailable') })
  await $.turn.complete(TURN)
  const log = [...files.values()].join('\n')
  expect(log).toContain('idle timer yielded to native idle compaction at 240000 tokens')
  expect(log).not.toContain('idle timer armed')
  await clock.advance(271_000)
  expect(calls.compacts).toBe(0)
})

test('does not re-arm after idle compact until a user prompt completes', async ($, on) => {
  const { calls, clock, files } = engine(on, 130_000, {}, 0, '{"enabled": true}', true)
  const log = () => [...files.entries()].find(([path]) => path.endsWith('auto-compact.log'))?.[1] ?? ''
  await $.turn.complete(TURN)
  await clock.advance(271_000)
  expect(calls.compacts).toBe(1)

  await $.prompt.submit({ text: WAKE_MARKER })
  await $.turn.complete({ ...TURN, turnId: 't-resume' })
  expect(log()).toContain('idle timer not re-armed: no user prompt since idle compact')
  expect(log().match(/idle timer armed at 130000 tokens/g)?.length).toBe(1)

  await $.prompt.submit({ text: 'continue' })
  await $.turn.complete({ ...TURN, turnId: 't-user-without-asUser' })
  expect(log().match(/idle timer armed at 130000 tokens/g)?.length).toBe(2)
  await clock.advance(271_000)
  expect(calls.compacts).toBe(2)

  await $.prompt.submit({ text: 'continue', asUser: true })
  await $.turn.complete({ ...TURN, turnId: 't-user' })
  expect(log().match(/idle timer armed at 130000 tokens/g)?.length).toBe(3)

  await clock.advance(271_000)
  await $.prompt.submit({ text: WAKE_MARKER, origin: { kind: 'user' } })
  await $.turn.complete({ ...TURN, turnId: 't-user-marker' })
  expect(log().match(/idle timer armed at 130000 tokens/g)?.length).toBe(4)
})

test('does not arm the idle timer below 100K tokens', async ($, on) => {
  const { calls, clock } = engine(on, 99_999)
  await $.turn.complete(TURN)
  await clock.advance(271_000)
  expect(calls.compacts).toBe(0)
})

test('a prompt before the cache timer fires prevents compaction', async ($, on) => {
  const { calls, clock } = engine(on, 130_000)
  await $.turn.complete(TURN)
  await clock.advance(60_000)
  await $.prompt.submit({ text: 'continue', asUser: true })
  await clock.advance(270_000)
  expect(calls.compacts).toBe(0)
})

test('the five minute cache timer compacts just before its TTL', async ($, on) => {
  const { calls, clock } = engine(on, 130_000)
  await $.turn.complete(TURN)
  await clock.advance(269_999)
  expect(calls.compacts).toBe(0)
  await clock.advance(1)
  await clock.advance(1_000)
  expect(calls.compacts).toBe(1)
})

test('a one hour cache write arms the idle timer with the long TTL', async ($, on) => {
  const { calls, clock } = engine(on, 130_000)
  const writes: string[] = []
  on('fs.write', async (_$, e) => { writes.push(e.text); return { value: undefined } })
  await $.turn.complete({ ...TURN, usage: { model: 'test', input_tokens: 0, output_tokens: 0, cache_creation: { ephemeral_1h_input_tokens: 1, ephemeral_5m_input_tokens: 0 } } })
  expect(writes.join('\n')).toContain('idle timer armed at 130000 tokens, TTL 3600000ms')
  await clock.advance(3_299_999)
  expect(calls.compacts).toBe(0)
  await clock.advance(1)
  expect(calls.compacts).toBe(0)
  await clock.advance(1_000)
  expect(calls.compacts).toBe(1)
  await clock.advance(3_600_000)
  expect(calls.compacts).toBe(1)
})

test('short chained timers reach the one hour cache deadline', async ($, on) => {
  const { calls, clock } = engine(on, 130_000)
  await $.turn.complete({ ...TURN, usage: { model: 'test', input_tokens: 0, output_tokens: 0, cache_creation: { ephemeral_1h_input_tokens: 1, ephemeral_5m_input_tokens: 0 } } })
  for (let minute = 0; minute < 54; minute++) await clock.advance(60_000)
  expect(calls.compacts).toBe(0)
  await clock.advance(60_000)
  await clock.advance(1_000)
  expect(calls.compacts).toBe(1)
})

test('a five minute cache write keeps the default idle timer TTL', async ($, on) => {
  const { calls, clock } = engine(on, 130_000)
  const writes: string[] = []
  on('fs.write', async (_$, e) => { writes.push(e.text); return { value: undefined } })
  await $.turn.complete({ ...TURN, usage: { model: 'test', input_tokens: 0, output_tokens: 0, cache_read_input_tokens: 0, cache_creation_input_tokens: 1, cache_creation: { ephemeral_1h_input_tokens: 0, ephemeral_5m_input_tokens: 1 } } })
  expect(writes.join('\n')).toContain('idle timer armed at 130000 tokens, TTL 300000ms')
  await clock.advance(269_999)
  expect(calls.compacts).toBe(0)
  await clock.advance(1)
  await clock.advance(1_000)
  expect(calls.compacts).toBe(1)
})

test('main-turn writes without a TTL breakdown keep the long idle timer across cache reads', async ($, on) => {
  const { calls, clock } = engine(on, 130_000)
  const writes: string[] = []
  on('fs.write', async (_$, e) => { writes.push(e.text); return { value: undefined } })
  await $.turn.complete({ ...TURN, usage: { model: 'test', input_tokens: 0, output_tokens: 1, cache_read_input_tokens: 82_611, cache_creation_input_tokens: 559 } })
  expect(writes[writes.length - 1]).toContain('idle timer armed at 130000 tokens, TTL 3600000ms')
  await $.prompt.submit({ text: 'next', asUser: true })
  await $.turn.complete({ ...TURN, turnId: 't-read', usage: { model: 'test', input_tokens: 0, output_tokens: 1, cache_read_input_tokens: 83_170, cache_creation_input_tokens: 0 } })
  expect(writes[writes.length - 1]).toContain('idle timer armed at 130000 tokens, TTL 3600000ms')
  await clock.advance(3_299_000)
  expect(calls.compacts).toBe(0)
  await clock.advance(1_000)
  expect(calls.compacts).toBe(0)
  await clock.advance(1_000)
  expect(calls.compacts).toBe(1)
})

test('an aborted turn consumes the pending long-gap inference', async ($, on) => {
  const { calls, clock } = engine(on, 130_000)
  await $.turn.complete(TURN)
  await $.prompt.submit({ text: 'cancel the pending idle timer', asUser: true })
  await clock.advance(300_001)
  await $.prompt.submit({ text: 'next', asUser: true })
  await $.turn.complete({ ...TURN, turnId: 't-aborted', reason: 'aborted', isAborted: true, usage: { model: 'test', input_tokens: 0, output_tokens: 0, cache_read_input_tokens: 1, cache_creation_input_tokens: 0 } })
  await $.turn.complete({ ...TURN, turnId: 't-later', usage: { model: 'test', input_tokens: 0, output_tokens: 0, cache_read_input_tokens: 1, cache_creation_input_tokens: 0 } })
  await clock.advance(269_999)
  expect(calls.compacts).toBe(0)
  await clock.advance(1)
  await clock.advance(1_000)
  expect(calls.compacts).toBe(1)
})

test('infers a one hour cache TTL from cache usage after a long idle gap', async ($, on) => {
  const { calls, clock } = engine(on, 130_000)
  await $.turn.complete(TURN)
  await $.prompt.submit({ text: 'cancel the pending idle timer', asUser: true })
  await clock.advance(300_001)
  await $.prompt.submit({ text: 'next', asUser: true })
  await $.turn.complete({ ...TURN, turnId: 't2', usage: { model: 'test', input_tokens: 0, output_tokens: 0, cache_read_input_tokens: 1, cache_creation_input_tokens: 0 } })
  await clock.advance(3_299_000)
  expect(calls.compacts).toBe(0)
  await clock.advance(1_000)
  expect(calls.compacts).toBe(0)
  await clock.advance(1_000)
  expect(calls.compacts).toBe(1)
})

test('the off switch disables it', async ($, on) => {
  const { calls, clock } = engine(on, 900_000, { PURSER_AUTO_COMPACT_TOKENS: '0' })
  await $.turn.complete(TURN)
  await clock.advance(20_000)
  expect(calls.compacts).toBe(0)
})

test('stays off without a recorded choice', async ($, on) => {
  const { calls, clock } = engine(on, 900_000, {}, 0, null)
  await $.turn.complete(TURN)
  await clock.advance(20_000)
  expect(calls.compacts).toBe(0)
})

test('uses CLAUDE_CONFIG_DIR for the opt-in and log', async ($, on) => {
  const { clock, reads } = engine(on, 260_000, { CLAUDE_CONFIG_DIR: '/custom/claude' })
  const writes: string[] = []
  on('fs.write', async (_$, e) => { writes.push(e.path); return { value: undefined } })
  await $.turn.complete(TURN)
  await clock.advance(1_000)
  expect(reads.map(normalized)).toContain('/custom/claude/purser/auto-compact.json')
  expect(writes.map(normalized)).toContain('/custom/claude/purser/auto-compact.log')
})

test('PURSER_STATE_DIR takes precedence for the opt-in and log', async ($, on) => {
  const { clock, reads } = engine(on, 260_000, { CLAUDE_CONFIG_DIR: '/custom/claude', PURSER_STATE_DIR: '/custom/state' })
  const writes: string[] = []
  on('fs.write', async (_$, e) => { writes.push(e.path); return { value: undefined } })
  await $.turn.complete(TURN)
  await clock.advance(1_000)
  expect(reads.map(normalized)).toContain('/custom/state/auto-compact.json')
  expect(writes.map(normalized)).toContain('/custom/state/auto-compact.log')
})

test('uses the default state root when neither state override is set', async ($, on) => {
  const { clock, reads } = engine(on, 260_000)
  await $.turn.complete(TURN)
  await clock.advance(1_000)
  expect(reads.map(normalized)).toContain('/home/.claude/purser/auto-compact.json')
})

test('stays off when init recorded no', async ($, on) => {
  const { calls, clock } = engine(on, 900_000, {}, 0, '{"enabled": false}')
  await $.turn.complete(TURN)
  await clock.advance(20_000)
  expect(calls.compacts).toBe(0)
})

test('stays off when the setting is unreadable', async ($, on) => {
  const { calls, clock } = engine(on, 900_000, {}, 0, 'junk')
  await $.turn.complete(TURN)
  await clock.advance(20_000)
  expect(calls.compacts).toBe(0)
})

test('retries a compact rejected while the turn winds down', async ($, on) => {
  const { calls, clock } = engine(on, 260_000, {}, 2)
  await $.turn.complete(TURN)
  await clock.advance(1_000)
  expect(calls.compacts).toBe(1)
  await clock.advance(3_000)
  expect(calls.compacts).toBe(2)
  await clock.advance(10_000)
  expect(calls.compacts).toBe(3)
})

test('keeps trying on later turns after every retry failed', async ($, on) => {
  const { calls, clock } = engine(on, 260_000, {}, 3)
  await $.turn.complete(TURN)
  await clock.advance(14_000)
  expect(calls.compacts).toBe(3)
  await $.turn.complete(TURN)
  await clock.advance(1_000)
  expect(calls.compacts).toBe(4)
})

test('a later completed turn can compact after turn-running refusals', async ($, on) => {
  const { calls, clock, logs } = engine(on, 260_000, {}, 3)
  await $.turn.complete(TURN)
  await $.prompt.submit({ text: 'next turn', asUser: true })
  await clock.advance(14_000)
  expect(calls.compacts).toBe(3)
  expect(logs.some(line => line.startsWith('Purser auto-compact:') && line.includes('a turn is running'))).toBe(false)

  await $.turn.complete({ ...TURN, turnId: 't2' })
  await clock.advance(1_000)
  expect(calls.compacts).toBe(4)
  expect(logs.some(line => line.includes('compacted at 260000 tokens'))).toBe(true)
})

test('writes each main turn decision to the auto-compact log', async ($, on) => {
  const { clock, files } = engine(on, 260_000, { USERPROFILE: 'C:/home', OS: 'Windows_NT' }, 0, '{"enabled": true}', true)
  await $.turn.complete(TURN)
  await clock.advance(1_000)
  const log = [...files.entries()].find(([path]) => path.endsWith('auto-compact.log'))?.[1]
  expect(log).toContain('260000 tokens, threshold 250000, compacting')
  expect(log).toContain('at 260000 tokens')
})

test('trace history survives a module reset and keeps events from two sessions', async ($, on) => {
  const { clock, files, setSessionId } = engine(on, 260_000, {}, 0, '{"enabled": true}', true)
  setSessionId('s1')
  await $.turn.complete(TURN)
  await clock.advance(1_000)
  const log = () => [...files.entries()].find(([path]) => path.endsWith('auto-compact.log'))?.[1]
  const firstHistory = log()!
  expect(firstHistory).toContain('[s1]')

  resetAutoCompactForTests()
  setSessionId('s2')
  await $.turn.complete({ ...TURN, turnId: 't2' })
  const history = log()!
  expect(history).toContain('[s1]')
  expect(history).toContain('[s2]')
  expect(history.split('\n').filter(Boolean).length).toBeGreaterThan(firstHistory.split('\n').filter(Boolean).length)
})

test('recognises the SDK-host refusal that needs a /compact prompt', () => {
  expect(needsCompactPrompt(new Error('HooksError: purser: $.session.compact: not available in a headless (-p / SDK) session yet'))).toBe(true)
  expect(needsCompactPrompt(new Error('a turn is running'))).toBe(false)
  expect(turnIsRunning(new Error('HooksError: purser: $.session.compact: a turn is running (t1)'))).toBe(true)
  expect(turnIsRunning(new Error('unrelated compact failure'))).toBe(false)
})

test('compact notice reports the measured saving; the visible wake line stays language-neutral', () => {
  const text = compactNotice(415_943, 83_000)
  expect(text).toContain('**Context passed 415K, so Purser ran /compact.**')
  expect(text).toContain('Every later message is now ~80% cheaper')
  expect(text.endsWith('from the handoff Purser prepared.*')).toBe(true)
  expect(compactNotice(415_943, undefined)).not.toContain('cheaper')
  expect(WAKE_MARKER).toBe('⚓ /compact ✓')
  const context = wakeContext(text)
  expect(context).toContain("in the user's language")
  expect(context).toContain(text)
  expect(context).toContain(RESUME_PROMPT)
})
