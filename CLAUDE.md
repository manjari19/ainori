# Working on Ainori with Manjari

Read `SPEC.md` first. It's the source of truth for scope, architecture, and milestones. If something in the spec looks wrong or unbuildable, say so and suggest a fix before writing code. Don't silently change the design.

## Who you're working with
Manjari is a third-year SFU CS student building this in one day for an Uber internship application. She has to explain and defend every part of it in interviews, so **her understanding matters as much as working code.** She learns best with a plain-English explanation and an analogy first, then the technical terms layered on top.

## How to run each milestone
1. Before coding, explain in 3 to 5 plain-English sentences what this milestone builds and why it exists in the system.
2. Build it in small steps. Run the code and the tests for real. Never claim something works without running it.
3. When it's done, append a section to `NOTES.md`:
   - What it does (plain English, one analogy)
   - Key design decisions and the tradeoff behind each
   - 3 likely interview questions with short model answers
4. **Explain-it-back quiz:** ask Manjari 3 questions about what was just built, one at a time. Wait for her answer, then correct or deepen it. Don't move on until she's answered all three.
5. Suggest a commit message and stop. **Do not run `git commit` or `git push`.** Manjari makes every commit herself.
6. Wait for her to say "next" before starting the next milestone.

## Rules
- Follow the milestone order in `SPEC.md` section 13. Keep an eye on time. If a milestone is running well over its budget, tell her and suggest the cut order from the spec.
- Only use the dependencies listed in `SPEC.md` section 4. Ask before adding anything else.
- Idiomatic Go: small packages, interfaces at the point of use, errors wrapped with context, `context.Context` passed through, graceful shutdown on SIGINT/SIGTERM. Comments only where the "why" isn't obvious.
- Tests are real tests against real Redis and Postgres (via Docker Compose locally, service containers in CI). Always run Go tests with `-race`.
- **Never invent numbers.** Latency, throughput, and match rate in the README or resume come only from saved simulator reports in `reports/`.
- If something can't run in your environment (no Docker, no network, an Oracle step), say so plainly and give Manjari the exact commands to run herself. Never fake the output.
- Secrets only in `.env` (gitignored), with a `.env.example` committed.
- Frontend: everything fully visible at phone width and desktop, with no overlapping text.
- Writing style for docs, README, and NOTES: human and plain, no em dashes, no buzzword filler.
