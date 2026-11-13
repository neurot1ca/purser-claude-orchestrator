import { platformBinary } from './read.js'

const reported = new Set<string>()
let binaryPath: Promise<string> | undefined

async function observeClaudeWork($, e) {
  const session = typeof e.session_id === 'string' ? e.session_id : '<unknown>'
  try {
    binaryPath ??= (async () => {
      const os = await $.env.get('OS')
      const ostype = await $.env.get('OSTYPE')
      const arch = await $.env.get('PROCESSOR_ARCHITECTURE')
      const hosttype = await $.env.get('HOSTTYPE')
      let goos = os === 'Windows_NT' ? 'windows' : /darwin/i.test(ostype ?? '') ? 'darwin' : /linux/i.test(ostype ?? '') ? 'linux' : ''
      let goarch = /^(amd64|x86_64)$/i.test(arch ?? hosttype ?? '') ? 'amd64' : /^(arm64|aarch64)$/i.test(arch ?? hosttype ?? '') ? 'arm64' : ''
      if (!goos || !goarch) {
        const uname = await $.process.run(['uname', '-s', '-m'], { timeoutMs: 1000 })
        if (uname.exitCode !== 0) throw new Error('platform detection failed')
        const [system, machine] = uname.stdout.trim().split(/\s+/)
        goos ||= /darwin/i.test(system ?? '') ? 'darwin' : /linux/i.test(system ?? '') ? 'linux' : ''
        goarch ||= /^(amd64|x86_64)$/i.test(machine ?? '') ? 'amd64' : /^(arm64|aarch64)$/i.test(machine ?? '') ? 'arm64' : ''
      }
      return `${$.plugin.root}/bin/${platformBinary(goos, goarch)}`
    })().catch(error => { binaryPath = undefined; throw error })
    const result = await $.process.run([await binaryPath, 'claude-work'], { cwd: e.cwd, stdin: JSON.stringify(e), timeoutMs: e.hook_event_name === 'SessionEnd' ? 700 : 250 })
    if (result.exitCode !== 0) throw new Error('Claude work observer failed')
  } catch {
    if (!reported.has(session)) {
      reported.add(session)
      try { await $.ui.log('Claude work observation failed; hook decision continued unchanged.') }
      catch { /* Observation logging must not affect hooks. */ }
    }
  }
}

async function forwardClaudeWork($, e, next) {
  const prior = await next(e)
  await observeClaudeWork($, e)
  return prior
}

export function registerClaudeWork(on) {
  on('classic.SubagentStart', forwardClaudeWork)
  on('classic.SubagentStop', forwardClaudeWork)
  on('classic.SessionEnd', forwardClaudeWork)
}
