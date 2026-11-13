import { expect, test } from 'claude-code/testing'

test('Claude work lifecycle forwards events and preserves prior hook results', async ($, on) => {
  const forwarded: string[] = []
  on('env.get', (_$, e) => ({ value: e.name === 'OS' ? 'Windows_NT' : e.name === 'PROCESSOR_ARCHITECTURE' ? 'AMD64' : undefined }))
  on('process.run', (_$, e) => {
    forwarded.push(JSON.parse(e.init.stdin).hook_event_name)
    expect(e.argv).toEqual([expect.stringContaining('purser-hook-windows-amd64.exe'), 'claude-work'])
    return { value: { exitCode: 0, stdout: '', stderr: '' } }
  })
  on('classic.SubagentStart', () => ({ additionalContext: ['prior start'] }))
  on('classic.SubagentStop', () => ({ block: 'prior stop' }))
  on('classic.SessionEnd', () => ({ additionalContext: ['prior end'] }))

  const start = await $.classic.SubagentStart({ hook_event_name: 'SubagentStart', session_id: 's', cwd: '/repo', agent_id: 'a', agent_type: 'dispatcher' })
  const stop = await $.classic.SubagentStop({ hook_event_name: 'SubagentStop', session_id: 's', cwd: '/repo', agent_id: 'a' })
  const end = await $.classic.SessionEnd({ hook_event_name: 'SessionEnd', session_id: 's', cwd: '/repo', reason: 'exit' })

  expect(start.additionalContext).toEqual(['prior start'])
  expect(stop.block).toBe('prior stop')
  expect(end.additionalContext).toEqual(['prior end'])
  expect(forwarded).toEqual(['SubagentStart', 'SubagentStop', 'SessionEnd'])
})

test('Claude work observer failure is logged and leaves hook decisions intact', async ($, on) => {
  const logs: string[] = []
  on('env.get', (_$, e) => ({ value: e.name === 'OS' ? 'Windows_NT' : e.name === 'PROCESSOR_ARCHITECTURE' ? 'AMD64' : undefined }))
  on('process.run', () => ({ deny: 'observer unavailable' }))
  on('ui.log', (_$, e) => { logs.push(e.text); return { value: undefined } })
  on('classic.SubagentStop', () => ({ block: 'existing decision' }))
  expect((await $.classic.SubagentStop({ hook_event_name: 'SubagentStop', session_id: 's' })).block).toBe('existing decision')
  expect(logs).toEqual(['Claude work observation failed; hook decision continued unchanged.'])
})

