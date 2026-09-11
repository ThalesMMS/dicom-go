# Offline documentation validation

The repository provides one documentation-integrity command for both Go
modules:

```sh
make docs-check
```

Each module also runs that target from `make check`. The checker is implemented
in `dicom-go/cmd/doccheck` so both modules use identical parsing and policy.
It does not request external URLs. Configured Go help commands run with module
downloads and proxy variables disabled, an exact four-argument allowlist, a
bounded output buffer, and a short timeout.

The module-local `.doccheck.json` files define the stable validation surface:

- `include` lists authoritative Markdown files or source directories. Build
  outputs and caches are intentionally absent.
- `exemptions` names an exact file or narrow glob, the exempt rule, and a
  required current reason. An exemption for historical issue provenance does
  not disable link or anchor validation.
- `current_sections` lists documents or headings whose issue references must
  remain current. `allowed_issues` is the explicit offline baseline; update it
  only after verifying issue state.
- `path_references` pairs selected literal documentation text with a
  repository-relative target. This covers normative paths written in prose or
  code spans without guessing that every code span is a path.
- `help_commands` pairs one exact fenced command with its expected help text
  and timeout. Arbitrary fenced commands are never executed.

Diagnostics use `repository/path.md:line: doccheck/rule: message`. Local links
are URL-decoded, constrained to the repository (including resolved symlinks),
and checked for existence. Markdown fragments are compared with GitHub-style
anchors, including duplicate and Setext headings. Reference-style links,
images, inline-code boundaries, and fenced-code boundaries are handled without
opening the network.

Historical or third-party documents remain in `include` whenever their local
links should stay healthy. Add an exemption only for a rule whose current-state
semantics do not apply, and always state why. Generated Markdown should either
be excluded by using authoritative source roots or receive a narrow, reasoned
exemption when its links still belong in the gate.
