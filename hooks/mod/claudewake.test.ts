import { expect, mock, test } from 'claude-code/testing'
import { CLAUDE_WAKE_MARKER, claudeWakeContext, registerClaudeWake } from './quotawake.js'

function engine(on, resetDelay: number) {
  const clock = mock.clock(on)
  const resetAt = clock.now() + resetDelay
  const submitted: { text: string; context?: readonly string[]; origin?: { kind?: string } }[] = []
  on('session.usage', () => ({ value: {
    context: { window: 200_000 },
    rateLimits: [
      { kind: 'five_hour', percentUsed: 100, resetsAt: new Date(resetAt).toISOString() },
      { kind: 'seven_day', percentUsed: 40, resetsAt: new Date(resetAt + 2_000_000).toISOString() },
    ],
  } }))
  on('prompt.submit', (_$, e) => {
    submitted.push({ text: e.text, context: e.context, origin: e.origin })
    return { text: e.text, context: e.context }
  })
  on('classic.StopFailure', { error: 'rate_limit' }, async () => ({}))
  on('classic.UserPromptSubmit', { session_id: 's1' }, async () => ({}))
  on('turn.complete', async () => ({ text: 'ok' }))
  return { clock, submitted }
}

test('schedules a wake after a rate-limit stop and submits the approved marker', async ($, on) => {
  const { clock, submitted } = engine(on, 3_600_000)
  await $.classic.StopFailure({ hook_event_name: 'StopFailure', session_id: 's1', error: 'rate_limit' })
  await clock.advance(3_600_000)
  expect(submitted.length).toBe(0)
  await clock.advance(61_000)
  expect(submitted.length).toBe(1)
  expect(submitted[0]!.text).toBe(CLAUDE_WAKE_MARKER)
})

test('does not schedule on a normal turn', async ($, on) => {
  const { clock, submitted } = engine(on, 60_000)
  await $.turn.complete({ answer: 'done', durationMs: 1, isAborted: false, turnId: 't1', reason: 'answer' })
  await clock.advance(200_000)
  expect(submitted.length).toBe(0)
})

test('fires only once for the same reset', async ($, on) => {
  const { clock, submitted } = engine(on, 60_000)
  await $.classic.StopFailure({ hook_event_name: 'StopFailure', session_id: 's1', error: 'rate_limit' })
  await $.classic.StopFailure({ hook_event_name: 'StopFailure', session_id: 's1', error: 'rate_limit' })
  await clock.advance(121_000)
  await $.classic.StopFailure({ hook_event_name: 'StopFailure', session_id: 's1', error: 'rate_limit' })
  await clock.advance(121_000)
  expect(submitted.length).toBe(1)
})

test('cancels before reset when the user sends a message', async ($, on) => {
  const { clock, submitted } = engine(on, 60_000)
  await $.classic.StopFailure({ hook_event_name: 'StopFailure', session_id: 's1', error: 'rate_limit' })
  await $.classic.UserPromptSubmit({ hook_event_name: 'UserPromptSubmit', session_id: 's1', prompt: 'I will continue manually' })
  await clock.advance(200_000)
  expect(submitted.length).toBe(0)
})

test('attaches hidden context only to a plugin-origin marker', async ($, on) => {
  const markerHooks: ((...args: any[]) => Promise<any>)[] = []
  registerClaudeWake((...args) => { if (args[0] === 'prompt.submit') markerHooks.push(args[2]) })
  const hook = markerHooks[0]!
  const userMarker = await hook($, { text: CLAUDE_WAKE_MARKER, context: [], origin: { kind: 'user' } }, async e => e)
  expect(userMarker.context).not.toContain(claudeWakeContext())
  const pluginMarker = await hook($, { text: CLAUDE_WAKE_MARKER, context: [], origin: { kind: 'plugin' } }, async e => e)
  expect(pluginMarker.context).toContain(claudeWakeContext())
})
