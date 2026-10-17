type ToolSchema = {
  name: string
  description: string
  inputSchema: {
    type: 'object'
    properties: Record<string, Record<string, unknown>>
    required: string[]
  }
}

const baseProperties = {
  prompt: { type: 'string' },
  label: { type: 'string', description: "Task label in user's language (up to 80 characters)." },
  model: { type: 'string' },
  cwd: { type: 'string' },
  config: { type: 'object' },
  'developer-instructions': { type: 'string' },
  'base-instructions': { type: 'string' },
  'compact-prompt': { type: 'string' },
  sandbox: { type: 'string', enum: ['read-only', 'workspace-write', 'danger-full-access'] },
  'approval-policy': { type: 'string', enum: ['on-request', 'never'] },
}

export const CODEX_TOOL_SCHEMAS: ToolSchema[] = [
  { name: 'codex', description: 'Run a new thread synchronously; include the ORCH first line.', inputSchema: { type: 'object', properties: baseProperties, required: ['prompt'] } },
  { name: 'codex-reply', description: 'Continue a server thread synchronously; supply its threadId.', inputSchema: { type: 'object', properties: {
    threadId: { type: 'string' },
    prompt: { type: 'string' }, label: { type: 'string', description: "Task label in user's language; omitted reuses the thread label (up to 80 characters)." },
  }, required: ['threadId', 'prompt'] } },
  { name: 'codex_start', description: 'Start a durable job; include ORCH. Resume with mode=reply and threadId; follow next.', inputSchema: { type: 'object', properties: {
    ...baseProperties, mode: { type: 'string', enum: ['new', 'reply'] }, worktree: { type: 'boolean' },
    threadId: { type: 'string' }, timeoutSeconds: { type: 'number', exclusiveMinimum: 0 },
    etaMinutes: { type: 'integer' },
  }, required: ['prompt'] } },
  { name: 'codex_result', description: 'Finalize a completed job and return its review bundle; follow start.next first.', inputSchema: { type: 'object', properties: {
    jobId: { type: 'string' }, adopt: { type: 'boolean', description: 'After user consent, finalize a job from an earlier session in this project.' },
  }, required: ['jobId'] } },
  { name: 'codex_cancel', description: 'Stop a running job or discard an unapplied finished job.', inputSchema: { type: 'object', properties: {
    jobId: { type: 'string' }, reason: { type: 'string' },
  }, required: ['jobId'] } },
  { name: 'codex_apply', description: 'Apply a reviewed job patch; acceptOnly records acceptance without applying.', inputSchema: { type: 'object', properties: {
    jobId: { type: 'string' }, resolved: { type: 'boolean', description: 'Record a hand-resolved conflict after verifying the repository.' },
    reviewer1Valid: { type: 'integer', minimum: 0, description: 'Reviewer valid count.' }, reviewer1False: { type: 'integer', minimum: 0, description: 'Reviewer false count.' },
    acceptOnly: { type: 'boolean' }, revision: { type: 'string', description: 'Reviewed changed-paths digest; required with acceptOnly.' }
  }, required: ['jobId'] } },
]

type LegacyMcpResult = {
  content: [{ type: 'text'; text: string }]
  structuredContent?: Record<string, unknown>
  isError: boolean
}

// Python json.dumps(..., ensure_ascii=False) uses spaces after separators.
function pythonJson(value: unknown): string {
  const compact = JSON.stringify(value)
  if (compact === undefined) throw new TypeError('value is not JSON serializable')
  let result = ''
  let quoted = false
  let escaped = false
  for (const char of compact) {
    if (quoted) {
      result += char
      if (escaped) escaped = false
      else if (char === '\\') escaped = true
      else if (char === '"') quoted = false
    } else if (char === '"') {
      quoted = true
      result += char
    } else {
      result += char === ',' || char === ':' ? `${char} ` : char
    }
  }
  return result
}

export function renderCodexToolResult(input: {
  thread_id: string | null
  content?: string | null
  error?: string | null
  usage?: Record<string, unknown> | null
}): LegacyMcpResult {
  const failed = input.error !== undefined && input.error !== null
  const value: Record<string, unknown> = failed
    ? { threadId: input.thread_id, error: input.error }
    : { threadId: input.thread_id, content: input.content ?? null }
  if (!failed && input.usage && Object.keys(input.usage).length) value.usage = input.usage
  return {
    content: [{ type: 'text', text: pythonJson(value) }],
    ...(failed ? {} : { structuredContent: value }),
    isError: failed,
  }
}

export function renderCodexJobResult(value: Record<string, unknown>): LegacyMcpResult {
  return {
    content: [{ type: 'text', text: pythonJson(value) }],
    structuredContent: value,
    isError: Object.hasOwn(value, 'errorCode'),
  }
}
