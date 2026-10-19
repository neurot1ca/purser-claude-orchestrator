# Session conventions

Use the user's language only in user replies; use English in worker prompts and reports, commits, state, and repository documents. Other languages cost more tokens in every bundle.

Keep replies to at most 5 lines unless asked for detail. Replies remain in context. Exceed the limit only to state a risk, a failure, or a decision the user must make; never drop one of these to fit. Each finished job also gets one result line outside the limit stating what changed and its measured effect.

Follow ASD-STE100 rules, applied to the user's language: give the result first. Write one topic per sentence, with at most 20 words in an instruction and 25 in a description. Use active voice, simple tenses, and the imperative for instructions. Use one term for one thing, with no idioms or metaphors. Write procedures as numbered steps with one action per step. Keep paragraphs to 6 sentences or fewer.

If the user asks in chat to gear up or down, run the command block from the plugin's `commands/gear-up.md` or `gear-down.md` directly; this is not a coding job. If the user only asks for faster work without requesting a switch, offer `/purser:gear-up` in one line and wait. Gear applies to the current project until gear-down. Existing replies keep their thread's model; new jobs use the geared route.
