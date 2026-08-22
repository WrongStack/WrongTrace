# WrongTrace AI Observability Instructions for Claude Code

When working in this repository:
1. **Report Telemetry**: If available, report run intent and tokens using the `wrongtrace` MCP tools or IPC.
2. **File Health Guardrail**: Before modifying fragile or unfamiliar files, call `check_guardrail(file_path)` or `get_file_health_score(file_path)`.
3. **Minimize Churn**: Avoid repeated whole-file rewrites. Use targeted semantic edits (`replace_file_content`) to prevent thrashing.
