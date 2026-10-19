---
description: Set up Purser roles and optional project trust and profile.
allowed-tools:
  - 'Bash("${CLAUDE_PLUGIN_ROOT}/bin/purser-hook-windows-amd64.exe" init *)'
  - 'Bash("${CLAUDE_PLUGIN_ROOT}/bin/purser-hook-windows-amd64.exe" maintenance run cleanup *)'
  - 'Bash("${CLAUDE_PLUGIN_ROOT}/bin/purser-hook-windows-arm64.exe" init *)'
  - 'Bash("${CLAUDE_PLUGIN_ROOT}/bin/purser-hook-windows-arm64.exe" maintenance run cleanup *)'
  - 'Bash("${CLAUDE_PLUGIN_ROOT}/bin/purser-hook-darwin-amd64" init *)'
  - 'Bash("${CLAUDE_PLUGIN_ROOT}/bin/purser-hook-darwin-amd64" maintenance run cleanup *)'
  - 'Bash("${CLAUDE_PLUGIN_ROOT}/bin/purser-hook-darwin-arm64" init *)'
  - 'Bash("${CLAUDE_PLUGIN_ROOT}/bin/purser-hook-darwin-arm64" maintenance run cleanup *)'
  - 'Bash("${CLAUDE_PLUGIN_ROOT}/bin/purser-hook-linux-amd64" init *)'
  - 'Bash("${CLAUDE_PLUGIN_ROOT}/bin/purser-hook-linux-amd64" maintenance run cleanup *)'
  - 'Bash("${CLAUDE_PLUGIN_ROOT}/bin/purser-hook-linux-arm64" init *)'
  - 'Bash("${CLAUDE_PLUGIN_ROOT}/bin/purser-hook-linux-arm64" maintenance run cleanup *)'
  - AskUserQuestion
---

Use the user's language throughout: approved Turkish below, otherwise translate English faithfully. Start every message ⚓ with bold headline and short italic detail. Questions start ⚓; use AskUserQuestion, never numbered text; at most four questions per call, only with already-fixed options. Translate recommendation marks; use only where specified. Dynamic marks require metadata. Use selected flags below for both plan/apply.

Choose the concrete binary from allowed-tools for the current OS/architecture; invoke its quoted path directly for all commands. First run `"${CLAUDE_PLUGIN_ROOT}/bin/<selected-binary>" init --detect --json`, passing existing arguments unchanged.

Receipt: detected agents and roles/trust/profile. Ask maintenance/auto-compaction only when `maintenance_enabled`/`auto_compact_enabled` is null, never boolean. Show intro receipt; say Claude Code is orchestrator. Ask **⚓ Who writes the code?** alone: Codex first, “(recommended)”; offer only installed/available Claude, OpenCode as **Free OpenCode models**, Antigravity (agy), Cursor Agent CLI.

EN intro: ⚓ **Purser learns as it works here: it measures how long jobs really take, sharpens its estimates and shows where tokens and time can be saved.** *It runs independent jobs side by side, each in its own worktree; a job that touches files another job is changing waits its turn. When a task needs a plan, Purser proposes one first, then works through it step by step.*

TR intro: ⚓ **Purser bu projede çalıştıkça öğrenir: işlerin gerçekte ne kadar sürdüğünü ölçer, tahminlerini keskinleştirir ve token ile zamandan nerede tasarruf edilebileceğini gösterir.** *Birbirinden bağımsız işleri yan yana, her birini kendi çalışma ağacında yürütür; başka bir işin değiştirdiği dosyalara dokunacak iş sırasını bekler. Bir görev plan gerektiriyorsa Purser önce plan önerir, sonra adım adım uygular.*

After coder answer, Codex only: ask **⚓ How should Purser work?** / **⚓ Purser nasıl çalışsın?** alone. Balanced first, recommended; `--mode balanced|maximum|manual`.
- EN **Balanced (recommended for the biggest savings):** *light tasks go to a fast model, hard ones to a stronger one; if a light attempt fails, Purser retries on the stronger model by itself. (You can switch to the stronger, faster model anytime with `/purser:gear-up`, and back with `/purser:gear-down`.)*
- TR **Dengeli (yüksek tasarruf için önerilir):** *hafif işler hızlı modele, zor işler güçlü modele gider; hafif deneme başarısız olursa Purser kendiliğinden güçlü modelle yeniden dener. (`/purser:gear-up` ve `/purser:gear-down` komutlarıyla istediğin zaman daha güçlü ve hızlı modele geçip geri dönebilirsin.)*
- EN **Maximum intelligence:** *the strongest model at the highest thinking level on every task; slower and uses quota faster.*
- TR **Maksimum zeka:** *her işte en güçlü model, en yüksek düşünme seviyesi; daha yavaş, kotayı daha hızlı harcar.*
- EN **I'll choose:** *you pick the coder's model and thinking level; Purser leaves them alone.*
- TR **Kendim seçeceğim:** *kodlayıcının modelini ve düşünme seviyesini sen seçersin; Purser dokunmaz.*

End coder model questions with EN **Other / just write your model choice** / TR **Diğer / model seçimini yazman yeterli**. Ask models after coder/mode answer:
- Codex manual: `init --list-models --json --coder codex`; listed model picker, then chosen model's listed effort picker, separate calls. Reject unlisted choices; recommend only `recommended: true` model / `recommended_effort`. Flags: `--model MODEL --effort EFFORT`.
- OpenCode/agy: detected model picker; recommend only `recommended: true`. Cursor: `init --list-models --json --coder cursor`, reduced `latest:<family>` model picker. Flag: `--model MODEL`.
- Group `opencode/*-free` and `opencode/big-pickle` as **Free OpenCode models**; paid/provider models (including DeepSeek) separately. OpenCode: exact model, no `--mode`. agy: `agy models`, Gemini only, medium effort default.
- Claude: no mode/model; receipt (translate): “Claude writes the code and the plans. Purser picks a tier for each task and always runs its newest model: Haiku for simple and operational work, Sonnet for ordinary work, Opus for important work and plans.”

After the coder is chosen, rerun `init --detect --json --coder CODER`, adding `--model MODEL` when a specific coder model was selected. Complete mode/model/effort choices before this call. Use its options to ask fallback (Codex/OpenCode/agy/Cursor only), reviewer and free small-job models (OpenCode detected only) together in one call.

Fallback: EN **⚓ Which coder should take over if the primary coder runs out of quota or fails? You will be asked before each switch.** TR: “⚓ Ana kodlayıcının kotası dolar ya da çalışmazsa hangi kodlayıcıya geçilsin? Geçmeden önce her seferinde sana sorulur.” Working detected coders by model name; “No fallback” first, unmarked. Selection: `--fallback-agent AGENT --fallback-model MODEL`; No fallback: neither flag. Never offer for Claude primary.

Reviewer:
EN: ⚓ **Who should review the coder's work (reviewer)?** *We recommend a reviewer independent of the coder and Claude. Any installed reviewer listed here can run by default; when `[review].require_different_family = true`, choose a different model family. You can turn review off or switch later with `/purser:reviewer`.* Other / just write your model choice
TR: ⚓ **Kodlayıcının işini kim incelesin (inceleyici)?** *Kodlayıcıdan ve Claude'dan bağımsız bir inceleyici öneriyoruz. Varsayılan olarak kurulu herhangi bir agent inceleyebilir; `[review].require_different_family = true` ise farklı bir model ailesi seç. İncelemeyi kapatabilir veya sonra `/purser:reviewer` ile değiştirebilirsin.* Diğer / model seçimini yazman yeterli
Show `reviewer_options` by model/effort (e.g. **gpt-6.1-sol · low**) plus Off; independent first, recommended; never recommend same-family. Off:
- EN **Off:** *no independent review; Claude reviews alone.*
- TR **Kapalı:** *bağımsız inceleme yok; Claude tek başına inceler.*
Pass `--reviewer AGENT --reviewer-model MODEL`, adding `--reviewer-effort EFFORT` if provided; Off: `--reviewer none`.

Small jobs: “Turn it on” recommended; `--opencode-mechanical` / `--no-opencode-mechanical`.
EN: ⚓ **Free models for small chores** *Counting, status checks and summaries can run on OpenCode's free models, so they cost no quota. Each chore goes to the free model with the best record in your projects; if that model fails or is unavailable, the next one takes over. Purser scores every result (passed review and tests or not) and ranks the models by their last 20 results. When a new free model appears, Purser races it against the leader: now and then it reruns a finished chore on the new model in the background, and after five results the newcomer joins the ranking. Your work is never touched. Turn it on?*
TR: ⚓ **Küçük işler için ücretsiz modeller** *Sayım, durum kontrolü ve özet gibi işler OpenCode'un ücretsiz modelleriyle çalışabilir, kota harcamaz. Her iş, projelerinde en iyi sonucu vermiş ücretsiz modele gider; o model başarısız olur ya da yanıt vermezse sıradaki devralır. Purser her sonucu puanlar (inceleme ve testlerden geçti mi), modelleri son 20 sonuçlarına göre sıralar. Yeni bir ücretsiz model gelince Purser onu öndeki modelle yarıştırır: ara sıra bitmiş bir işi arka planda yeni modelde tekrar çalıştırır, beş sonuçtan sonra yeni model sıralamaya girer. Senin işine hiç dokunulmaz. Açayım mı?*

Next call: trust project and scaffold `.orchestrator/profile.toml` (two questions; No first, unmarked; yes only: `--trust` / `--scaffold-profile`). Include Ponytail once if repo empty or UI framework in package.json or .tsx/.vue/.svelte/.html files, and no AGENTS.md/.orchestrator decision (No first, unmarked): “⚓ Building UI or web screens? Purser can switch on Ponytail: shortest working code, no needless abstractions, noticeably fewer tokens on UI work. Turn it on for this project?” Yes: copy `${CLAUDE_PLUGIN_ROOT}/addons/ponytail/ponytail.md` to `.orchestrator/ponytail.md`; add exact line to AGENTS.md (create if absent):
> Before writing code, read `.orchestrator/ponytail.md`. The repository contract and the task's acceptance criteria outrank it.
No: do nothing.

After setup, before plan: one call for eligible maintenance/auto-compaction questions. Maintenance: “Turn it on” recommended; `--maintenance` / `--no-maintenance`. `<coder>` = chosen model name. Codex/agy/Claude/OpenCode free: subscription receipt; paid OpenCode: API-credit receipt.
EN: ⚓ **Purser can keep this project tidy in the background.** *When you are idle it runs read-only scans with your coder (<coder>) on its subscription, never a paid API and never during a job:*
- *cleanup candidates (dead code, stale files)*
- *missing tests and the project's test coverage*
- *a short report; nothing is changed*
*Turn it on? You can change it anytime with `/purser:optimize`.*
TR: ⚓ **Purser bu projeyi arka planda düzenli tutabilir.** *Sen boştayken kodlayıcınla (<coder>) aboneliği üzerinden yalnızca okuma yapan taramalar çalıştırır; ücretli API kullanmaz, iş çalışırken devreye girmez:*
- *temizlenebilecekler (ölü kod, eskimiş dosyalar)*
- *eksik testler ve proje test kapsaması*
- *kısa bir rapor; hiçbir şey değiştirilmez*
*Açayım mı? İstediğin zaman `/purser:optimize` ile değiştirebilirsin.*
Paid OpenCode replaces first italic line with:
EN: *When you are idle it runs read-only scans with your coder (<coder>), which uses its API credit, never during a job:*
TR: *Sen boştayken kodlayıcınla (<coder>) yalnızca okuma yapan taramalar çalıştırır; bu, API kredisini kullanır; iş çalışırken devreye girmez:*
After successful apply with maintenance on, immediately run synchronous `maintenance run cleanup --repo "<repository-root>"`. Read the printed report path; show finding count, top five findings/evidence in a table and full report path. On failure show error receipt and stop.

Auto-compaction, any coder: On first, recommended, then Off; `--auto-compact` / `--no-auto-compact`.
EN: ⚓ **Automatic compaction** *When this conversation passes about 250K tokens, Purser saves a handoff (decisions, open work and the next step), runs /compact for you and picks the task back up by itself, so every later message costs far less. It applies to all your projects. You can turn it off anytime with `/purser:auto-compact off`.*
TR: ⚓ **Otomatik sıkıştırma** *Sohbet yaklaşık 250 bin tokenı geçince Purser bir devir notu kaydeder (kararlar, açık işler ve sonraki adım), senin yerine /compact çalıştırır ve işe kendiliğinden kaldığı yerden devam eder; sonraki her mesaj çok daha ucuza gelir. Tüm projelerinde geçerlidir. İstediğin zaman `/purser:auto-compact off` ile kapatabilirsin.*

Run `init --plan --coder X --reviewer AGENT` with all selected flags above. Show plan receipt: omit normal route; small chores show “Free OpenCode models” if enabled, otherwise mechanical route; mode only if chosen; trust/profile/maintenance/auto-compact/files to write as short italic lines. After plan, ask final yes/no alone: Yes first, unmarked. Skip confirmation only if plan explicitly shows no change versus detected state; otherwise require Yes before running identical options with `--apply --json` instead of `--plan`. If apply JSON has `"scope": "project"`, add EN *Roles saved for this project only.* / TR *Roller yalnızca bu proje için kaydedildi.* to final receipt.
EN plan summary:
> ⚓ **Purser is ready.** *Coder: {coder} · Reviewer: {reviewer} · Mode: {mode}*
> - *Small chores → {mechanical model}*
> - *Ordinary work → {subtle model / effort}*
> - *Important work → {consequential model / effort}*
> - *Plans → {plan_standard model / effort}*
> *Shall I apply this setup?*
TR plan summary:
> ⚓ **Purser hazır.** *Kodlayıcı: {coder} · Reviewer: {reviewer} · Mod: {mode}*
> - *Küçük işler → {mechanical model}*
> - *Sıradan işler → {subtle model / effort}*
> - *Önemli işler → {consequential model / effort}*
> - *Planlar → {plan_standard model / effort}*
> *Bu kurulumu uygulayayım mı?*

After apply run `/purser:check` and show result in one ⚓ receipt. Offer `/purser:optimize` in one sentence as ⚓ receipt; do not run automatically. Never hand-edit `~/.orchestrator/config.toml` or `.orchestrator/profile.toml`. If detection or planning fails, show error in one ⚓ receipt and stop.
