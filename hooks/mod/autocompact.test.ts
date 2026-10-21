import { expect, mock, test } from 'claude-code/testing'
import { compactNotice, DEFAULT_THRESHOLD, needsCompactPrompt, resetAutoCompactForTests, RESUME_PROMPT, thresholdFrom, WAKE_MARKER, wakeContext } from './autocompact.js'

const TURN = { answer: 'ok', durationMs: 10, isAborted: false, turnId: 't1', reason: 'answer' } as const
const normalized = (path: string) => path.replace(/\\/g, '/').replace(/(^|:)[A-Za-z]:(?=\/)/g, '$1')

function engine(on, tokens: number, env: Record<string, string> = {}, failures = 0, setting: string | null = '{"enabled": true}') {
  resetAutoCompactForTests()
  const calls = { compacts: 0 }
  const reads: string[] = []
  const clock = mock.clock(on)
  mock.env(on, { OS: 'Linux', HOME: '/home', ...env })
  on('fs.read', async (_$, e) => {
    if (setting === null || !/auto-compact.json$/.test(e.path)) throw new Error('ENOENT')
    reads.push(e.path)
    return { value: setting }
  })
  on('turn.complete', async () => ({ text: 'ok' }))
  on('prompt.submit', async (_$, e) => ({ text: e.text, origin: e.origin }))
  on('session.usage', async () => ({ value: { startedAt: 0, context: { tokens, window: 1_000_000 }, rateLimits: [] } }))
  on('session.compact', async (_$, e) => {
    calls.compacts++
    if (calls.compacts <= failures) throw new Error('a turn is running')
    return { messages: [{ role: 'user', text: 'compacted', toolUses: [] }] }
  })
  return { calls, clock, reads }
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

test('arms the idle timer only from half threshold through just below threshold', async ($, on) => {
  const { calls, clock } = engine(on, 130_000)
  await $.turn.complete(TURN)
  await clock.advance(271_000)
  expect(calls.compacts).toBe(1)
})

test('does not arm the idle timer below half threshold', async ($, on) => {
  const { calls, clock } = engine(on, 124_999)
  await $.turn.complete(TURN)
  await clock.advance(271_000)
  expect(calls.compacts).toBe(0)
})

test('a prompt before the cache timer fires prevents compaction', async ($, on) => {
  const { calls, clock } = engine(on, 130_000)
  await $.turn.complete(TURN)
  await $.prompt.submit({ text: 'continue', asUser: true })
  await clock.advance(270_000)
  expect(calls.compacts).toBe(0)
})

test('the cache timer compacts at the default TTL minus its margin', async ($, on) => {
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
  await clock.advance(3_570_000)
  expect(calls.compacts).toBe(0)
  await clock.advance(1_000)
  expect(calls.compacts).toBe(1)
})

test('a five minute cache write keeps the default idle timer TTL', async ($, on) => {
  const { calls, clock } = engine(on, 130_000)
  const writes: string[] = []
  on('fs.write', async (_$, e) => { writes.push(e.text); return { value: undefined } })
  await $.turn.complete({ ...TURN, usage: { model: 'test', input_tokens: 0, output_tokens: 0, cache_creation: { ephemeral_1h_input_tokens: 0, ephemeral_5m_input_tokens: 1 } } })
  expect(writes.join('\n')).toContain('idle timer armed at 130000 tokens, TTL 300000ms')
  await clock.advance(269_999)
  expect(calls.compacts).toBe(0)
  await clock.advance(1)
  await clock.advance(1_000)
  expect(calls.compacts).toBe(1)
})

test('cache creation after a long idle gap does not infer a one hour TTL', async ($, on) => {
  const { calls, clock } = engine(on, 130_000)
  await $.turn.complete(TURN)
  await $.prompt.submit({ text: 'cancel the pending idle timer', asUser: true })
  await clock.advance(300_001)
  await $.prompt.submit({ text: 'next', asUser: true })
  await $.turn.complete({ ...TURN, turnId: 't-write', usage: { model: 'test', input_tokens: 0, output_tokens: 0, cache_read_input_tokens: 0, cache_creation_input_tokens: 1 } })
  await clock.advance(269_999)
  expect(calls.compacts).toBe(0)
  await clock.advance(1)
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
  await clock.advance(3_570_000)
  expect(calls.compacts).toBe(0)
  await clock.advance(29_000)
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

test('writes each main turn decision to the auto-compact log', async ($, on) => {
  const { clock } = engine(on, 260_000, { USERPROFILE: 'C:/home', OS: 'Windows_NT' })
  const writes: Record<string, string> = {}
  on('fs.write', async (_$, e) => {
    writes[e.path] = e.text
    return { value: undefined }
  })
  await $.turn.complete(TURN)
  await clock.advance(1_000)
  const log = Object.entries(writes).find(([path]) => path.endsWith('auto-compact.log'))?.[1]
  expect(log).toContain('260000 tokens, threshold 250000, compacting')
  expect(log).toContain('at 260000 tokens')
})

test('recognises the SDK-host refusal that needs a /compact prompt', () => {
  expect(needsCompactPrompt(new Error('HooksError: purser: $.session.compact: not available in a headless (-p / SDK) session yet'))).toBe(true)
  expect(needsCompactPrompt(new Error('a turn is running'))).toBe(false)
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
