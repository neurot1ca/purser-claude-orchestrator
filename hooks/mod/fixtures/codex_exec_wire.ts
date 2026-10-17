export const CODEX_EXEC_WIRE_FIXTURES = [
  {
    "name": "job_result_alias_unicode",
    "input": {
      "function": "_job_result",
      "value": {
        "conversationId": "oturum-ıd 🪁",
        "threadId": "işlem-çubuğu",
        "message": "Başarılı ✅"
      }
    },
    "expected_text": "{\"conversationId\": \"oturum-ıd 🪁\", \"threadId\": \"işlem-çubuğu\", \"message\": \"Başarılı ✅\"}"
  },
  {
    "name": "job_result_error",
    "input": {
      "function": "_job_result",
      "value": {
        "state": "FAILED",
        "errorCode": "INVALID_ARGUMENT",
        "message": "bad request"
      }
    },
    "expected_text": "{\"state\": \"FAILED\", \"errorCode\": \"INVALID_ARGUMENT\", \"message\": \"bad request\"}"
  },
  {
    "name": "job_result_success",
    "input": {
      "function": "_job_result",
      "value": {
        "jobId": "job-123",
        "state": "SUCCEEDED",
        "content": "done"
      }
    },
    "expected_text": "{\"jobId\": \"job-123\", \"state\": \"SUCCEEDED\", \"content\": \"done\"}"
  },
  {
    "name": "schema_codex",
    "input": {
      "function": "schema",
      "tool": "codex"
    },
    "expected_text": "{\"name\": \"codex\", \"description\": \"Run a new thread synchronously; include the ORCH first line.\", \"inputSchema\": {\"type\": \"object\", \"properties\": {\"prompt\": {\"type\": \"string\"}, \"label\": {\"type\": \"string\", \"description\": \"Task label in user's language (up to 80 characters).\"}, \"model\": {\"type\": \"string\"}, \"cwd\": {\"type\": \"string\"}, \"config\": {\"type\": \"object\"}, \"developer-instructions\": {\"type\": \"string\"}, \"base-instructions\": {\"type\": \"string\"}, \"compact-prompt\": {\"type\": \"string\"}, \"sandbox\": {\"type\": \"string\", \"enum\": [\"read-only\", \"workspace-write\", \"danger-full-access\"]}, \"approval-policy\": {\"type\": \"string\", \"enum\": [\"on-request\", \"never\"]}}, \"required\": [\"prompt\"]}}"
  },
  {
    "name": "schema_codex_apply",
    "input": {
      "function": "schema",
      "tool": "codex_apply"
    },
    "expected_text": "{\"name\": \"codex_apply\", \"description\": \"Apply a reviewed job patch; acceptOnly records acceptance without applying.\", \"inputSchema\": {\"type\": \"object\", \"properties\": {\"jobId\": {\"type\": \"string\"}, \"resolved\": {\"type\": \"boolean\", \"description\": \"Record a hand-resolved conflict after verifying the repository.\"}, \"reviewer1Valid\": {\"type\": \"integer\", \"minimum\": 0, \"description\": \"Reviewer valid count.\"}, \"reviewer1False\": {\"type\": \"integer\", \"minimum\": 0, \"description\": \"Reviewer false count.\"}, \"acceptOnly\": {\"type\": \"boolean\"}, \"revision\": {\"type\": \"string\", \"description\": \"Reviewed changed-paths digest; required with acceptOnly.\"}}, \"required\": [\"jobId\"]}}"
  },
  {
    "name": "schema_codex_cancel",
    "input": {
      "function": "schema",
      "tool": "codex_cancel"
    },
    "expected_text": "{\"name\": \"codex_cancel\", \"description\": \"Stop a running job or discard an unapplied finished job.\", \"inputSchema\": {\"type\": \"object\", \"properties\": {\"jobId\": {\"type\": \"string\"}, \"reason\": {\"type\": \"string\"}}, \"required\": [\"jobId\"]}}"
  },
  {
    "name": "schema_codex_reply_alias",
    "input": {
      "function": "schema",
      "tool": "codex-reply"
    },
    "expected_text": "{\"name\": \"codex-reply\", \"description\": \"Continue a server thread synchronously; supply its threadId.\", \"inputSchema\": {\"type\": \"object\", \"properties\": {\"threadId\": {\"type\": \"string\"}, \"prompt\": {\"type\": \"string\"}, \"label\": {\"type\": \"string\", \"description\": \"Task label in user's language; omitted reuses the thread label (up to 80 characters).\"}}, \"required\": [\"threadId\", \"prompt\"]}}"
  },
  {
    "name": "schema_codex_result",
    "input": {
      "function": "schema",
      "tool": "codex_result"
    },
    "expected_text": "{\"name\": \"codex_result\", \"description\": \"Finalize a completed job and return its review bundle; follow start.next first.\", \"inputSchema\": {\"type\": \"object\", \"properties\": {\"jobId\": {\"type\": \"string\"}, \"adopt\": {\"type\": \"boolean\", \"description\": \"After user consent, finalize a job from an earlier session in this project.\"}}, \"required\": [\"jobId\"]}}"
  },
  {
    "name": "schema_codex_start",
    "input": {
      "function": "schema",
      "tool": "codex_start"
    },
    "expected_text": "{\"name\": \"codex_start\", \"description\": \"Start a durable job; include ORCH. Resume with mode=reply and threadId; follow next.\", \"inputSchema\": {\"type\": \"object\", \"properties\": {\"prompt\": {\"type\": \"string\"}, \"label\": {\"type\": \"string\", \"description\": \"Task label in user's language (up to 80 characters).\"}, \"model\": {\"type\": \"string\"}, \"cwd\": {\"type\": \"string\"}, \"config\": {\"type\": \"object\"}, \"developer-instructions\": {\"type\": \"string\"}, \"base-instructions\": {\"type\": \"string\"}, \"compact-prompt\": {\"type\": \"string\"}, \"sandbox\": {\"type\": \"string\", \"enum\": [\"read-only\", \"workspace-write\", \"danger-full-access\"]}, \"approval-policy\": {\"type\": \"string\", \"enum\": [\"on-request\", \"never\"]}, \"mode\": {\"type\": \"string\", \"enum\": [\"new\", \"reply\"]}, \"worktree\": {\"type\": \"boolean\"}, \"threadId\": {\"type\": \"string\"}, \"timeoutSeconds\": {\"type\": \"number\", \"exclusiveMinimum\": 0}, \"etaMinutes\": {\"type\": \"integer\"}}, \"required\": [\"prompt\"]}}"
  },
  {
    "name": "tool_result_alias_unicode",
    "input": {
      "function": "_tool_result",
      "thread_id": "iş parçacığı 🧵",
      "content": "Merhaba, dünya! 🚀"
    },
    "expected_text": "{\"threadId\": \"iş parçacığı 🧵\", \"content\": \"Merhaba, dünya! 🚀\"}"
  },
  {
    "name": "tool_result_error",
    "input": {
      "function": "_tool_result",
      "thread_id": null,
      "error": "request failed"
    },
    "expected_text": "{\"threadId\": null, \"error\": \"request failed\"}"
  },
  {
    "name": "tool_result_success",
    "input": {
      "function": "_tool_result",
      "thread_id": "thread-123",
      "content": "completed"
    },
    "expected_text": "{\"threadId\": \"thread-123\", \"content\": \"completed\"}"
  },
  {
    "name": "tool_result_usage_absent",
    "input": {
      "function": "_tool_result",
      "thread_id": "thread-usage",
      "content": "done",
      "usage": {}
    },
    "expected_text": "{\"threadId\": \"thread-usage\", \"content\": \"done\"}"
  },
  {
    "name": "tool_result_usage_present",
    "input": {
      "function": "_tool_result",
      "thread_id": "thread-usage",
      "content": "done",
      "usage": {
        "input_tokens": 12,
        "output_tokens": 3
      }
    },
    "expected_text": "{\"threadId\": \"thread-usage\", \"content\": \"done\", \"usage\": {\"input_tokens\": 12, \"output_tokens\": 3}}"
  }
] as const
