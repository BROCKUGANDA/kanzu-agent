## Summary

<!-- One sentence describing what this PR changes and why. -->

## Checklist

- [ ] `go build -mod=vendor ./...` passes locally
- [ ] `go run ./cmd/kanzu doctor` reports `ok` for every check
- [ ] `golangci-lint run ./...` is clean (or new findings are justified)
- [ ] No `net/http` added to `cmd/` or `internal/` (offline claim)
- [ ] No KES / Kenya references introduced (submission is UGX / Uganda only)
- [ ] `metadata.json` and `submission.json` are in sync

## Type of change

- [ ] Bug fix (corrects a rule, ledger, or narration defect)
- [ ] New compliance rule (FATF-aligned typology; update `internal/rules/`)
- [ ] Knowledge base addition (English / Kiswahili / Luganda)
- [ ] Trilingual narration / planning prompt refinement
- [ ] TUI / UX improvement (Bubble Tea layer)
- [ ] CI / release plumbing
- [ ] Documentation / metadata sync

## Impact on the cross-disciplinary pairing

<!-- Does this PR change a deterministic (rule-engine) or model path?
     Every flagging decision must remain deterministic; the model only
     narrates. State that explicitly. -->

## Notes for the reviewer

<!-- Anything that needs a second look, or links to supporting evidence. -->
