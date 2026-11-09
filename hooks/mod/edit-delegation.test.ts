import { expect, test } from 'claude-code/testing'

function stub(on, outcome: { exitCode?: number; stdout?: string }, calls: unknown[], logs: string[], worker = '') {
  on('env.get', (_$, e) => ({ value: ({ OS: 'Windows_NT', PROCESSOR_ARCHITECTURE: 'AMD64', PURSER_WORKER: worker })[e.name] }))
  on('session.id', () => ({ value: 'edit-session' }))
  on('session.cwd', () => ({ value: 'C:/repo' }))
  on('process.run', (_$, e) => {
    expect(e.init.timeoutMs).toBe(2500)
    calls.push(JSON.parse(e.init.stdin))
    return { value: { exitCode: outcome.exitCode ?? 0, stdout: outcome.stdout ?? '', stderr: '' } }
  })
  on('ui.log', (_$, e) => { logs.push(e.text); return { value: undefined } })
  on('tool.call', (_$, e) => ({ result: JSON.stringify(e) }))
}

test('edit guard maps Go deny before the edit', async ($, on) => {
  const calls: unknown[] = []
  stub(on, { stdout: JSON.stringify({ hookSpecificOutput: {
    hookEventName: 'PreToolUse', permissionDecision: 'deny', permissionDecisionReason: 'delegate this change',
  } }) }, calls, [])
  const result = await $.tool.call({ tool: 'Write', file_path: 'source.go', content: 'package main' })
  expect(result.isError).toBe(true)
  expect(result.text).toContain('delegate this change')
  expect(calls).toHaveLength(1)
  expect(calls[0]).toMatchObject({ tool_name: 'Write', agent_id: '', tool_input: { file_path: 'source.go' } })
})

test('shell edit guard maps Go deny before Bash', async ($, on) => {
  const calls: unknown[] = []
  stub(on, { stdout: JSON.stringify({ hookSpecificOutput: {
    hookEventName: 'PreToolUse', permissionDecision: 'deny', permissionDecisionReason: 'delegate shell change',
  } }) }, calls, [])
  const result = await $.tool.call({ tool: 'Bash', command: "sed -i 's/a/b/g' source.go" })
  expect(result.isError).toBe(true)
  expect(result.text).toContain('delegate shell change')
  expect(calls).toHaveLength(1)
  expect(calls[0]).toMatchObject({ tool_name: 'Bash', tool_input: { command: "sed -i 's/a/b/g' source.go" } })
})

test('shell guard skips commands without write shapes', async ($, on) => {
  const calls: unknown[] = []
  stub(on, { exitCode: 0 }, calls, [])
  const result = await $.tool.call({ tool: 'Bash', command: 'ls -la 2>/dev/null' })
  expect(result.isError).not.toBe(true)
  expect(calls).toEqual([])
})

test('shell guard prefilters case-insensitive cmdlets and adjacent redirects', async ($, on) => {
  const calls: unknown[] = []
  stub(on, { exitCode: 0 }, calls, [])
  await $.tool.call({ tool: 'PowerShell', command: 'set-content -Path source.go -Value x' })
  await $.tool.call({ tool: 'Bash', command: 'echo x>source.go' })
  expect(calls).toHaveLength(2)
})

test('shell guard exempts subagents before Go', async ($, on) => {
  const calls: unknown[] = []
  stub(on, { exitCode: 1 }, calls, [])
  await $.tool.call({ tool: 'PowerShell', command: 'Set-Content -Path source.go -Value x', agentId: 'agent-1' })
  expect(calls).toEqual([])
})

test('edit guard fails open and reports only once per session', async ($, on) => {
  const calls: unknown[] = [], logs: string[] = []
  stub(on, { exitCode: 1 }, calls, logs)
  for (let i = 0; i < 2; i++) {
    const result = await $.tool.call({ tool: 'Edit', file_path: 'source.go', old_string: 'a', new_string: 'b' })
    expect(result.isError).not.toBe(true)
  }
  expect(calls).toHaveLength(2)
  expect(logs).toHaveLength(1)
})

test('edit guard exempts worker processes before invoking Go', async ($, on) => {
  const calls: unknown[] = []
  stub(on, { exitCode: 1 }, calls, [], '1')
  await $.tool.call({ tool: 'MultiEdit', file_path: 'source.go', edits: [] })
  expect(calls).toEqual([])
})

test('edit guard allows malformed output', async ($, on) => {
  stub(on, { stdout: '{broken' }, [], [])
  const result = await $.tool.call({ tool: 'NotebookEdit', notebook_path: 'work.ipynb', new_source: 'x' })
  expect(result.isError).not.toBe(true)
})
